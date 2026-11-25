package runtime

import (
	"context"
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
	Page   string
	Offset int
}

func (r *Runtime) list(ctx context.Context, q *model.ListSessionsRequest, out *model.ListSessionsResponse) error {
	var cursor pageCursor
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Offset < 0 {
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
		if !filepath.IsAbs(q.WorkingDirectory) {
			return errors.New("working directory must be absolute")
		}
		params["cwd"] = q.WorkingDirectory
	}
	if cursor.Page != "" {
		params["cursor"] = cursor.Page
	}
	page, err := rpc[struct {
		NextCursor string
		Sessions   []struct{ SessionID, Title string }
	}](ctx, p, "session/list", params)
	if err != nil {
		return err
	}
	if cursor.Offset > len(page.Sessions) {
		return errors.New("session list changed since the last page")
	}
	rest := page.Sessions[cursor.Offset:]
	next := pageCursor{Page: page.NextCursor}
	if q.Limit > 0 && uint64(q.Limit) < uint64(len(rest)) {
		rest = rest[:q.Limit]
		next = pageCursor{Page: cursor.Page, Offset: cursor.Offset + len(rest)}
	}
	for _, s := range rest {
		out.Sessions = append(out.Sessions, &model.Session{Ref: &model.SessionRef{HarnessInstance: r.config.HarnessInstance, SessionId: s.SessionID}, Capabilities: caps, DisplayName: s.Title})
	}
	if next.Page != "" || next.Offset > 0 {
		b, _ := json.Marshal(next)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return nil
}
