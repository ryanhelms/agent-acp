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
	acp "github.com/coder/acp-go-sdk"
)

type empty struct{}
type peer struct {
	runtime *Runtime
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *os.File
	conn    *acp.Connection
	exited  chan struct{}
	once    sync.Once
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
	AgentCapabilities struct {
		LoadSession         bool
		SessionCapabilities struct{ List, Resume *empty }
		PromptCapabilities  struct{ Image, Audio bool }
	}
}

func rpc[T any](ctx context.Context, p *peer, method string, params any) (T, error) {
	result, err := acp.SendRequest[T](p.conn, ctx, method, params)
	if err != nil {
		return result, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

func (r *Runtime) connect(ctx context.Context, handle acp.MethodHandler) (*peer, handshake, error) {
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
	if err = cmd.Start(); err != nil {
		r.mu.Unlock()
		in.Close()
		out.Close()
		childOut.Close()
		return nil, init, err
	}
	childOut.Close()
	p := &peer{runtime: r, cmd: cmd, in: in, out: out, exited: make(chan struct{})}
	p.conn = acp.NewConnection(handle, p, out)
	diagnostics := r.config.Stderr
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	p.conn.SetLogger(slog.New(slog.NewTextHandler(diagnostics, &slog.HandlerOptions{Level: slog.LevelWarn})))
	r.peers[p] = true
	r.mu.Unlock()
	go func() { _ = cmd.Wait(); close(p.exited) }()
	init, err = rpc[handshake](ctx, p, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "agent-acp", "version": "1"}, "clientCapabilities": empty{}})
	if err != nil {
		p.close()
		return nil, init, err
	}
	if init.ProtocolVersion != 1 {
		p.close()
		return nil, init, fmt.Errorf("unsupported ACP protocol version %d", init.ProtocolVersion)
	}
	return p, init, nil
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
	agent := init.AgentCapabilities
	return &model.Capabilities{
		CanResume: agent.LoadSession || agent.SessionCapabilities.Resume != nil,
		CanAnswer: true, CanInterrupt: true, CanListSessions: agent.SessionCapabilities.List != nil,
		CanReadHistory: r.config.ReadHistory != nil || (r.config.ReplayHistory && agent.LoadSession),
		CanPromptImage: agent.PromptCapabilities.Image, CanPromptAudio: agent.PromptCapabilities.Audio,
	}
}

func (r *Runtime) mcpServers() []map[string]any {
	out := make([]map[string]any, 0, len(r.config.MCPServers))
	for _, server := range r.config.MCPServers {
		names := make([]string, 0, len(server.Env))
		for n := range server.Env {
			names = append(names, n)
		}
		sort.Strings(names)
		env := make([]map[string]string, 0, len(names))
		for _, name := range names {
			env = append(env, map[string]string{"name": name, "value": server.Env[name]})
		}
		out = append(out, map[string]any{"name": server.Name, "command": server.Command, "args": append([]string{}, server.Args...), "env": env})
	}
	return out
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
	})
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
