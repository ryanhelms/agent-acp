package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	model "agent-acp.local/sdks/go/model"
	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
)

type empty struct{}
type peer struct {
	runtime *Runtime
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *os.File
	conn    *transport.Connection
	exited  chan struct{}
	once    sync.Once
	version int
	// Observe completion of the prompt write so session/cancel cannot overtake it.
	writeMu    sync.Mutex
	nextPrompt chan struct{}
}

func (p *peer) Write(data []byte) (int, error) {
	n, err := p.in.Write(data)
	p.writeMu.Lock()
	if p.nextPrompt != nil {
		var header struct{ Method string }
		if json.Unmarshal(data, &header) == nil && header.Method == "session/prompt" {
			close(p.nextPrompt)
			p.nextPrompt = nil
		}
	}
	p.writeMu.Unlock()
	return n, err
}

func (p *peer) readyPrompt() <-chan struct{} {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.nextPrompt = make(chan struct{})
	return p.nextPrompt
}

type handshake struct {
	ProtocolVersion   int
	AgentInfo         struct{ Name string }
	Info              struct{ Name string }
	AgentCapabilities struct {
		LoadSession         bool
		SessionCapabilities struct{ List, Resume, SystemPrompt *empty }
		PromptCapabilities  struct{ Image, Audio, EmbeddedContext bool }
		MCPCapabilities     struct{ ACP bool }
	}
	Capabilities struct {
		Session *struct {
			Prompt       struct{ Image, Audio, EmbeddedContext *empty }
			SystemPrompt *empty
			MCP          struct{ Stdio, ACP *empty }
		}
	}
}

func rpc[T any](ctx context.Context, p *peer, method string, params any) (T, error) {
	result, err := transport.SendRequest[T](p.conn, ctx, method, params)
	if err != nil {
		return result, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

// The ACP library uses fixed diagnostic messages. Its attributes can include an
// entire frame, arbitrary request IDs, peer methods, and errors containing data.
func transportLogger(output io.Writer) *slog.Logger {
	if output == nil {
		output = io.Discard
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: slog.LevelWarn,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey, slog.LevelKey, slog.MessageKey:
				return a
			}
			return slog.Attr{}
		},
	}))
}

func (r *Runtime) connect(ctx context.Context, handle transport.MethodHandler, selectInbound func(method, sessionID string) bool) (*peer, handshake, error) {
	return r.connectWithOptions(ctx, handle, selectInbound, transport.Options{})
}

func (r *Runtime) connectWithOptions(ctx context.Context, handle transport.MethodHandler, selectInbound func(method, sessionID string) bool, opts transport.Options) (*peer, handshake, error) {
	var init handshake
	if r.config.Command == "" {
		return nil, init, errors.New("live operations require an adapter command")
	}
	cmd := exec.Command(r.config.Command, r.config.Args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = r.config.WorkingDirectory
	cmd.Env = cmd.Environ()
	for k, v := range r.config.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = r.config.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, init, err
	}
	out, childOut, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, init, err
	}
	cmd.Stdout = childOut
	// Own the stdout pipe so Wait cannot discard a final response still in transit.
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		in.Close()
		out.Close()
		childOut.Close()
		return nil, init, errors.New("runtime is closed")
	}
	// Configure sanitized diagnostics and receipt scope before the child can
	// emit any frames, including unsolicited pre-handshake notifications.
	p := &peer{runtime: r, cmd: cmd, in: in, out: out, exited: make(chan struct{})}
	opts.Logger = transportLogger(r.config.Stderr)
	if selectInbound != nil {
		opts.InboundMethods = []string{"session/update", "session/request_permission"}
		opts.SelectInbound = selectInbound
		opts.OmitPlainNotifications = true
	}
	p.conn = transport.NewConnectionWithOptions(handle, p, out, opts)
	if err = cmd.Start(); err != nil {
		r.mu.Unlock()
		in.Close()
		out.Close()
		childOut.Close()
		return nil, init, err
	}
	childOut.Close()
	r.peers[p] = true
	r.mu.Unlock()
	go func() { _ = cmd.Wait(); close(p.exited) }()
	version := r.config.ProtocolVersion
	if version == 0 {
		version = 1
	}
	init, err = rpc[handshake](ctx, p, "initialize", initializeParams(version))
	if err != nil {
		p.close()
		return nil, init, err
	}
	if init.ProtocolVersion < 1 || init.ProtocolVersion > version {
		p.close()
		return nil, init, fmt.Errorf("unsupported ACP protocol version %d", init.ProtocolVersion)
	}
	if version == 2 && init.ProtocolVersion == 1 {
		init, err = rpc[handshake](ctx, p, "initialize", initializeParams(1))
		if err != nil || init.ProtocolVersion != 1 {
			p.close()
			if err != nil {
				return nil, init, err
			}
			return nil, init, fmt.Errorf("adapter selected ACP %d when finalizing v1 negotiation", init.ProtocolVersion)
		}
	}
	p.version = init.ProtocolVersion
	return p, init, nil
}

func initializeParams(version int) map[string]any {
	info := map[string]string{"name": "agent-acp", "version": "1"}
	if version == 2 {
		return map[string]any{"protocolVersion": version, "info": info, "capabilities": empty{}}
	}
	return map[string]any{"protocolVersion": version, "clientInfo": info, "clientCapabilities": empty{}}
}

func (p *peer) disconnected() bool {
	select {
	case <-p.conn.Done():
		return true
	default:
		return false
	}
}

func (p *peer) close() {
	p.once.Do(func() {
		_ = p.in.Close()
		select {
		case <-p.exited:
		case <-time.After(time.Second):
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
		_ = p.out.Close()
		p.runtime.mu.Lock()
		delete(p.runtime.peers, p)
		p.runtime.mu.Unlock()
	})
}

func (r *Runtime) capabilities(init handshake) *model.Capabilities {
	if init.ProtocolVersion == 2 {
		session := init.Capabilities.Session
		if session == nil {
			return &model.Capabilities{}
		}
		return &model.Capabilities{
			CanAnswer: true, CanInterrupt: true, CanResume: true, CanListSessions: true,
			CanReadHistory: r.config.ReadHistory != nil || r.config.ReplayHistory,
			CanPromptImage: session.Prompt.Image != nil, CanPromptAudio: session.Prompt.Audio != nil,
			CanPromptEmbeddedContext: session.Prompt.EmbeddedContext != nil,
		}
	}
	agent := init.AgentCapabilities
	return &model.Capabilities{
		CanResume: agent.LoadSession || agent.SessionCapabilities.Resume != nil,
		CanAnswer: true, CanInterrupt: true, CanListSessions: agent.SessionCapabilities.List != nil,
		CanReadHistory: r.config.ReadHistory != nil || (r.config.ReplayHistory && agent.LoadSession),
		CanPromptImage: agent.PromptCapabilities.Image, CanPromptAudio: agent.PromptCapabilities.Audio,
		CanPromptEmbeddedContext: agent.PromptCapabilities.EmbeddedContext,
	}
}

func (r *Runtime) mcpServers(s *session, p *peer, init handshake) ([]map[string]any, error) {
	v2 := p.version == 2
	hasACP := init.AgentCapabilities.MCPCapabilities.ACP
	if v2 {
		hasACP = init.Capabilities.Session.MCP.ACP != nil
	}
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	out := make([]map[string]any, 0, len(r.config.MCPServers))
	for _, server := range r.config.MCPServers {
		if server.CallACP != nil && hasACP {
			if s.mcp.servers == nil {
				s.mcp.servers = make(map[string]*acpMCPRegistration)
				s.mcp.conn = p.conn
			}
			id := "mcp-" + identifier()
			s.mcp.servers[id] = &acpMCPRegistration{call: server.CallACP, active: make(map[string]struct{})}
			out = append(out, map[string]any{"type": "acp", "name": server.Name, "serverId": id})
			continue
		}
		if v2 && init.Capabilities.Session.MCP.Stdio == nil {
			return nil, errors.New("adapter does not advertise the configured MCP server transport")
		}
		out = append(out, stdioServerParams(server, p.version))
	}
	return out, nil
}

func stdioServerParams(server MCPServer, version int) map[string]any {
	names := make([]string, 0, len(server.Env))
	for n := range server.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	env := make([]map[string]string, 0, len(names))
	for _, name := range names {
		env = append(env, map[string]string{"name": name, "value": server.Env[name]})
	}
	entry := map[string]any{"name": server.Name, "command": server.Command, "args": append([]string{}, server.Args...), "env": env}
	if version == 2 {
		entry["type"] = "stdio"
	}
	return entry
}

type pageCursor struct {
	Scope  string `json:",omitempty"`
	Page   string
	Offset int
}

// An existing directory can have a symlink, case variant or alternate native
// spelling. Compare filesystem identity without opening any listed files.
// For a removed workspace only the identical absolute path is accepted.
func sameListedDirectory(requested string, info os.FileInfo, reported string) bool {
	if info == nil || !info.IsDir() || !filepath.IsAbs(reported) {
		return false
	}
	// A reported UNC share must not initiate a connection to an unrelated
	// network resource while validating a local or differently scoped query.
	volume := filepath.VolumeName(reported)
	if strings.HasPrefix(volume, `\\`) && !strings.EqualFold(volume, filepath.VolumeName(requested)) {
		return false
	}
	other, err := os.Stat(reported)
	return err == nil && other.IsDir() && os.SameFile(info, other)
}

func (r *Runtime) list(ctx context.Context, q *model.ListSessionsRequest, out *model.ListSessionsResponse) error {
	if q.WorkingDirectory != "" && !filepath.IsAbs(q.WorkingDirectory) {
		return errors.New("working directory must be absolute")
	}
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(q.HarnessInstance+"\x00"+q.WorkingDirectory)))
	var cursor pageCursor
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Offset < 0 || (cursor.Scope != "" && cursor.Scope != scope) {
			return errors.New("invalid session list cursor")
		}
	}
	p, init, err := r.connect(ctx, func(_ context.Context, method string, _ json.RawMessage) (any, *acp.RequestError) {
		return nil, acp.NewMethodNotFound(method)
	}, nil)
	if err != nil {
		return err
	}
	defer p.close()
	caps := r.capabilities(init)
	if !caps.CanListSessions {
		return errors.New("adapter does not support listing sessions")
	}
	params := map[string]any{}
	if q.WorkingDirectory != "" {
		params["cwd"] = q.WorkingDirectory
	}
	if cursor.Page != "" {
		params["cursor"] = cursor.Page
	}
	page, err := rpc[struct {
		NextCursor string
		Sessions   []struct{ SessionID, Title, Cwd string }
	}](ctx, p, "session/list", params)
	if err != nil {
		return err
	}
	if cursor.Offset > len(page.Sessions) {
		return errors.New("session list changed since the last page")
	}
	var directory os.FileInfo
	if q.WorkingDirectory != "" {
		directory, _ = os.Stat(q.WorkingDirectory)
		if directory != nil && !directory.IsDir() {
			return errors.New("session listing scope is not a directory")
		}
	}
	// A native page can include many sessions from the same directory.
	aliases := map[string]bool{}
	next := pageCursor{Scope: scope, Page: page.NextCursor}
	for index := cursor.Offset; index < len(page.Sessions); index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := page.Sessions[index]
		if q.WorkingDirectory != "" && item.Cwd != q.WorkingDirectory {
			match, seen := aliases[item.Cwd]
			if !seen {
				match = sameListedDirectory(q.WorkingDirectory, directory, item.Cwd)
				aliases[item.Cwd] = match
			}
			if !match {
				continue
			}
		}
		if q.Limit > 0 && uint64(len(out.Sessions)) >= uint64(q.Limit) {
			// Offset belongs to the original page, including excluded rows.
			next = pageCursor{Scope: scope, Page: cursor.Page, Offset: index}
			break
		}
		out.Sessions = append(out.Sessions, &model.Session{Ref: &model.SessionRef{HarnessInstance: r.config.HarnessInstance, SessionId: item.SessionID}, Capabilities: caps, DisplayName: item.Title})
	}
	if next.Page != "" && next.Page == cursor.Page && next.Offset <= cursor.Offset {
		return errors.New("session listing repeated its native cursor")
	}
	if next.Page != "" || next.Offset > 0 {
		b, _ := json.Marshal(next)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return nil
}
