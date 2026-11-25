// Package broker runs a verified ACP peer on behalf of multiple local processes.
// The caller supplies the authenticated launch registrations and owns feature
// qualification. A registration admits one runner with one session.
package broker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
)

const maxPeers = 8
const maxLanesPerPeer = 8

type Scope struct {
	Command, WorkingDirectory, ExecutableSHA256 string
	PolicyIdentity, CredentialIdentity          string
	Args, Env                                   []string
	ProtocolVersion                             int
}

// Launch contains the owner-selected peer and the exact permitted session/new
// params. Session-level credentials may differ across runners with the same
// process-level credential identity.
type Launch struct {
	Scope Scope
	New   json.RawMessage
}

type registration struct {
	key   [32]byte
	spec  Launch
	until time.Time
}

type Broker struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	l       net.Listener
	pending map[string]registration
	peers   map[[32]byte]*peer
	runners map[net.Conn]struct{}
}

func Listen(ctx context.Context, socket string) (*Broker, error) {
	if !filepath.IsAbs(socket) {
		return nil, errors.New("ACP broker requires an absolute socket path")
	}
	dir, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return nil, err
	}
	if !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, errors.New("ACP broker socket requires an owner-only directory")
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &Broker{ctx: ctx, cancel: cancel, l: l, pending: map[string]registration{}, peers: map[[32]byte]*peer{}, runners: map[net.Conn]struct{}{}}
	go b.accept()
	context.AfterFunc(ctx, b.Close)
	return b, nil
}

func (b *Broker) Register(spec Launch) (string, error) {
	scope := spec.Scope
	if !filepath.IsAbs(scope.Command) || !filepath.IsAbs(scope.WorkingDirectory) || scope.PolicyIdentity == "" ||
		scope.CredentialIdentity == "" || len(scope.ExecutableSHA256) != 2*sha256.Size || scope.ProtocolVersion != 1 && scope.ProtocolVersion != 2 ||
		len(spec.New) > 1<<20 {
		return "", errors.New("invalid ACP broker scope or setup")
	}
	if _, err := hex.DecodeString(scope.ExecutableSHA256); err != nil {
		return "", errors.New("invalid ACP executable pin")
	}
	var session struct{ Cwd string }
	if json.Unmarshal(spec.New, &session) != nil || session.Cwd != scope.WorkingDirectory {
		return "", errors.New("ACP broker setup does not match its workspace")
	}
	raw, err := canonical(spec.New)
	if err != nil {
		return "", err
	}
	spec.New = raw
	spec.Scope.Args = append([]string(nil), scope.Args...)
	spec.Scope.Env = append([]string{}, scope.Env...)
	key, _ := json.Marshal(spec.Scope)
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(secret[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		return "", b.ctx.Err()
	}
	now := time.Now()
	for existing, entry := range b.pending {
		if !entry.until.After(now) {
			delete(b.pending, existing)
		}
	}
	if len(b.pending) >= maxPeers*maxLanesPerPeer {
		return "", errors.New("ACP broker launch queue is full")
	}
	b.pending[ticket] = registration{sha256.Sum256(key), spec, now.Add(time.Minute)}
	return ticket, nil
}

func (b *Broker) Close() {
	b.cancel()
	_ = b.l.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for client := range b.runners {
		_ = client.Close()
	}
	for _, p := range b.peers {
		p.close()
	}
}

// Prewarm starts the physical process for a currently registered launch. The
// first attached runner supplies the exact wire initialization.
func (b *Broker) Prewarm(ticket string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pending[ticket]
	if !ok || time.Now().After(entry.until) || b.ctx.Err() != nil {
		return errors.New("ACP broker launch is unavailable")
	}
	_, err := b.peerLocked(entry)
	return err
}

func (b *Broker) accept() {
	for {
		conn, err := b.l.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.ctx.Err() != nil || len(b.runners) >= maxPeers*maxLanesPerPeer {
			b.mu.Unlock()
			_ = conn.Close()
			continue
		}
		b.runners[conn] = struct{}{}
		b.mu.Unlock()
		go b.handle(conn)
	}
}

func (b *Broker) handle(c net.Conn) {
	defer func() {
		_ = c.Close()
		b.mu.Lock()
		delete(b.runners, c)
		b.mu.Unlock()
	}()
	// A fixed-size, one-use ticket precedes the JSON-RPC stream. The owner
	// chooses the command and credentials before the runner connects.
	var ticket [64]byte
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, ticket[:]); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	b.mu.Lock()
	launch, ok := b.pending[string(ticket[:])]
	delete(b.pending, string(ticket[:]))
	if !ok || time.Now().After(launch.until) || b.ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	p, err := b.peerLocked(launch)
	if err != nil {
		b.mu.Unlock()
		return
	}
	p.mu.Lock()
	if p.lanes >= maxLanesPerPeer {
		p.mu.Unlock()
		b.mu.Unlock()
		return
	}
	p.lanes++
	p.mu.Unlock()
	b.mu.Unlock()

	runner := &lane{conn: c, peer: p, expectedNew: launch.spec.New, replies: map[string]chan frame{}}
	defer runner.close()
	runner.run()
}

func (b *Broker) peerLocked(entry registration) (*peer, error) {
	if p := b.peers[entry.key]; p != nil && p.ctx.Err() == nil {
		return p, nil
	}
	if len(b.peers) >= maxPeers {
		return nil, errors.New("ACP broker peer limit reached")
	}
	scope := entry.spec.Scope
	exe, err := os.Open(scope.Command)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(h, exe)
	_ = exe.Close()
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != scope.ExecutableSHA256 {
		return nil, errors.New("ACP broker executable no longer matches its pin")
	}
	ctx, cancel := context.WithCancel(b.ctx)
	cmd := exec.CommandContext(ctx, scope.Command, scope.Args...)
	cmd.Dir, cmd.Env = scope.WorkingDirectory, scope.Env
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, childOut, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		cancel()
		return nil, err
	}
	cmd.Stdout = childOut
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = childOut.Close()
		_ = out.Close()
		cancel()
		return nil, err
	}
	_ = childOut.Close()

	p := &peer{
		ctx: ctx, cancel: cancel, version: scope.ProtocolVersion,
		sessions: map[string]*lane{}, staged: map[string][]json.RawMessage{},
	}
	p.rpc = transport.NewConnectionWithOptions(p.inbound, observedWriter{in, p}, out, transport.Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		InboundMethods: []string{"session/update", "session/request_permission"},
		SelectInbound:  func(_, _ string) bool { return true },
	})
	b.peers[entry.key] = p
	go func() {
		<-p.rpc.Done()
		p.close()
	}()
	go func() {
		_ = cmd.Wait()
		_ = out.Close()
		p.close()
		b.mu.Lock()
		if b.peers[entry.key] == p {
			delete(b.peers, entry.key)
		}
		b.mu.Unlock()
	}()
	time.AfterFunc(30*time.Second, p.retireIdle)
	return p, nil
}

func canonical(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var entry map[string]any
	if err := d.Decode(&entry); err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errors.New("ACP broker expected an object")
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, errors.New("unexpected data after ACP broker object")
	}
	return json.Marshal(entry)
}

// Proxy forwards the ACP stdio stream for a registered worker.
func Proxy(ctx context.Context, socket, ticket string, stdin io.Reader, stdout io.Writer) error {
	if len(ticket) != 64 {
		return errors.New("invalid ACP broker ticket")
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if _, err := io.WriteString(c, ticket); err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(c, stdin)
		_ = c.(*net.UnixConn).CloseWrite()
	}()
	_, err = io.Copy(stdout, c)
	return err
}

type frame struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  json.RawMessage   `json:"params,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *acp.RequestError `json:"error,omitempty"`
}
