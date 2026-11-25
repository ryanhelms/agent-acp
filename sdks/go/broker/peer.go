package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
)

type peer struct {
	ctx        context.Context
	cancel     context.CancelFunc
	rpc        *transport.Connection
	version    int
	initMu     sync.Mutex
	initParams json.RawMessage
	initResult json.RawMessage

	mu          sync.Mutex
	sessions    map[string]*lane
	staged      map[string][]json.RawMessage
	stagedBytes int
	opening     int
	lanes       int
}

func (p *peer) close() { p.cancel() }

func (p *peer) retireIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lanes == 0 {
		p.close()
	}
}

func (p *peer) initialize(ctx context.Context, raw json.RawMessage) (json.RawMessage, *acp.RequestError) {
	params, err := canonical(raw)
	if err != nil {
		return nil, acp.NewInvalidParams("invalid ACP initialization")
	}
	p.initMu.Lock()
	defer p.initMu.Unlock()
	if p.initResult != nil {
		if !bytes.Equal(params, p.initParams) {
			return nil, acp.NewInvalidParams("ACP broker initialization does not match the shared peer")
		}
		return p.initResult, nil
	}
	var init struct{ ProtocolVersion int }
	if json.Unmarshal(params, &init) != nil || init.ProtocolVersion != p.version {
		return nil, acp.NewInvalidParams("ACP broker launch does not match the requested protocol version")
	}
	result, failure := p.request(ctx, "initialize", params)
	if failure != nil {
		return nil, failure
	}
	init.ProtocolVersion = 0
	if json.Unmarshal(result, &init) != nil || init.ProtocolVersion != p.version {
		p.close()
		return nil, acp.NewInvalidParams("shared ACP peer did not negotiate the qualified protocol version")
	}
	p.initParams, p.initResult = params, result
	return result, nil
}

func (p *peer) request(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, *acp.RequestError) {
	result, err := transport.SendRequest[json.RawMessage](p.rpc, ctx, method, raw)
	if err != nil {
		if request, ok := err.(*acp.RequestError); ok {
			return nil, request
		}
		return nil, acp.NewInternalError("ACP broker peer operation could not be confirmed")
	}
	return result, nil
}

func (p *peer) inbound(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	var body struct{ SessionID string }
	_ = json.Unmarshal(params, &body)
	receipt := transport.InboundReceipt(ctx, method, body.SessionID)
	if body.SessionID == "" || receipt == nil {
		return nil, acp.NewMethodNotFound(method)
	}
	p.mu.Lock()
	worker := p.sessions[body.SessionID]
	if worker == nil {
		if method == "session/update" && p.opening > 0 && len(receipt.Envelope)+p.stagedBytes <= 2<<20 {
			p.staged[body.SessionID] = append(p.staged[body.SessionID], receipt.Envelope)
			p.stagedBytes += len(receipt.Envelope)
		} else if method == "session/update" && p.opening > 0 {
			p.close()
		}
		p.mu.Unlock()
		if method == "session/request_permission" {
			return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
		}
		return nil, nil
	}
	p.mu.Unlock()
	if method == "session/update" {
		worker.sendRaw(receipt.Envelope)
		return nil, nil
	}
	return worker.forwardRequest(ctx, receipt, method)
}

type lane struct {
	conn        net.Conn
	peer        *peer
	expectedNew json.RawMessage
	ctx         context.Context
	cancel      context.CancelFunc

	mu           sync.Mutex
	out          chan outbound
	queuedBytes  atomic.Int64
	replies      map[string]chan frame
	operations   map[string]context.CancelFunc
	sessionID    string
	promptReady  chan struct{}
	initialized  bool
	nativeClosed bool
}

type outbound struct {
	raw            []byte
	closeAfterSend bool
}

func (l *lane) run() {
	l.ctx, l.cancel = context.WithCancel(l.peer.ctx)
	l.operations = map[string]context.CancelFunc{}
	l.out = make(chan outbound, 128)
	go l.writeOutput()
	stop := context.AfterFunc(l.ctx, func() { _ = l.conn.Close() })
	defer stop()

	in := bufio.NewScanner(l.conn)
	in.Buffer(make([]byte, 32768), 10<<20)
	for in.Scan() {
		var req frame
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return
		}
		switch {
		case req.Method == "":
			l.mu.Lock()
			if waiter := l.replies[string(req.ID)]; waiter != nil {
				delete(l.replies, string(req.ID))
				waiter <- req
			}
			l.mu.Unlock()
		case req.Method == "initialize":
			if l.initialized || len(req.ID) == 0 {
				return
			}
			out, failure := l.peer.initialize(l.ctx, req.Params)
			l.send(frame{ID: req.ID, Result: out, Error: failure})
			l.initialized = failure == nil
		case req.Method == "session/new":
			l.open(req)
		default:
			l.route(req)
		}
	}
}

func (l *lane) open(req frame) {
	params, err := canonical(req.Params)
	l.mu.Lock()
	alreadyOpened := l.sessionID != ""
	l.mu.Unlock()
	if !l.initialized || alreadyOpened || len(req.ID) == 0 || err != nil || !bytes.Equal(params, l.expectedNew) {
		l.send(frame{ID: req.ID, Error: acp.NewInvalidParams("ACP broker setup differs from the owner registration")})
		return
	}
	p := l.peer
	p.mu.Lock()
	p.opening++
	p.mu.Unlock()
	result, failure := p.request(l.ctx, req.Method, params)
	var opened struct{ SessionID string }
	if failure == nil && (json.Unmarshal(result, &opened) != nil || opened.SessionID == "") {
		failure = acp.NewInternalError("ACP broker received an invalid session identity")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() {
		p.opening--
		if p.opening == 0 {
			clear(p.staged)
			p.stagedBytes = 0
		}
	}()
	if failure != nil {
		l.send(frame{ID: req.ID, Error: failure})
		return
	}
	if p.sessions[opened.SessionID] != nil {
		l.send(frame{ID: req.ID, Error: acp.NewInternalError("ACP peer returned an already bound session")})
		return
	}
	l.mu.Lock()
	l.sessionID = opened.SessionID
	l.mu.Unlock()
	l.send(frame{ID: req.ID, Result: result})
	for _, raw := range p.staged[opened.SessionID] {
		l.sendRaw(raw)
		p.stagedBytes -= len(raw)
	}
	delete(p.staged, opened.SessionID)
	p.sessions[opened.SessionID] = l
}

func (l *lane) route(req frame) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if req.Method == "$/cancel_request" && len(req.ID) == 0 {
		var cancellation struct{ RequestID json.RawMessage }
		_ = json.Unmarshal(req.Params, &cancellation)
		if cancel := l.operations[string(cancellation.RequestID)]; cancel != nil {
			cancel()
		}
		return
	}
	var body struct{ SessionID string }
	if json.Unmarshal(req.Params, &body) != nil || body.SessionID == "" || body.SessionID != l.sessionID {
		if len(req.ID) != 0 {
			l.send(frame{ID: req.ID, Error: acp.NewInvalidParams("operation addresses an unrelated ACP session")})
		}
		return
	}
	switch req.Method {
	case "session/prompt", "session/set_config_option", "session/set_model":
		if len(req.ID) == 0 || l.operations[string(req.ID)] != nil || len(l.operations) >= 8 {
			_ = l.conn.Close()
			return
		}
		ctx, cancel := context.WithCancel(l.ctx)
		l.operations[string(req.ID)] = cancel
		if req.Method == "session/prompt" {
			if l.promptReady != nil {
				cancel()
				delete(l.operations, string(req.ID))
				l.send(frame{ID: req.ID, Error: acp.NewInvalidParams("a prompt is already active in this ACP session")})
				return
			}
			l.promptReady = make(chan struct{})
		}
		go func() {
			result, failure := l.peer.request(ctx, req.Method, req.Params)
			l.mu.Lock()
			if req.Method == "session/prompt" {
				l.finishPromptWrite()
				l.promptReady = nil
			}
			delete(l.operations, string(req.ID))
			l.mu.Unlock()
			l.send(frame{ID: req.ID, Result: result, Error: failure})
			cancel()
		}()
	case "session/cancel":
		if len(req.ID) == 0 {
			ready := l.promptReady
			go func() {
				if ready != nil {
					select {
					case <-ready:
					case <-l.ctx.Done():
						return
					}
				}
				_ = l.peer.rpc.SendNotification(l.ctx, req.Method, req.Params)
			}()
		}
	case "session/close":
		if len(req.ID) != 0 {
			go func() {
				out, failure := l.peer.request(l.ctx, req.Method, req.Params)
				if failure == nil {
					l.mu.Lock()
					l.nativeClosed = true
					l.mu.Unlock()
				}
				l.sendFrame(frame{ID: req.ID, Result: out, Error: failure}, failure == nil)
			}()
		}
	default:
		if len(req.ID) != 0 {
			l.send(frame{ID: req.ID, Error: acp.NewMethodNotFound(req.Method)})
		}
	}
}

func (l *lane) finishPromptWrite() {
	if l.promptReady == nil {
		return
	}
	select {
	case <-l.promptReady:
	default:
		close(l.promptReady)
	}
}

type observedWriter struct {
	io.Writer
	peer *peer
}

func (w observedWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if err != nil {
		return n, err
	}
	var header struct {
		Method string
		Params struct{ SessionID string }
	}
	if json.Unmarshal(data, &header) == nil && header.Method == "session/prompt" {
		w.peer.mu.Lock()
		l := w.peer.sessions[header.Params.SessionID]
		w.peer.mu.Unlock()
		if l != nil {
			l.mu.Lock()
			l.finishPromptWrite()
			l.mu.Unlock()
		}
	}
	return n, nil
}

func (l *lane) forwardRequest(ctx context.Context, receipt *transport.Receipt, method string) (any, *acp.RequestError) {
	if method != "session/request_permission" {
		return nil, acp.NewMethodNotFound(method)
	}
	id := string(receipt.ID)
	waiter := make(chan frame, 1)
	l.mu.Lock()
	if l.replies[id] != nil || len(l.replies) >= 16 {
		l.mu.Unlock()
		return nil, acp.NewInvalidParams("invalid or excessive concurrent ACP peer requests")
	}
	l.replies[id] = waiter
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.replies, id)
		l.mu.Unlock()
	}()
	l.sendRaw(receipt.Envelope)
	select {
	case response := <-waiter:
		return response.Result, response.Error
	case <-ctx.Done():
		params, _ := json.Marshal(map[string]json.RawMessage{"requestId": receipt.ID})
		l.send(frame{Method: "$/cancel_request", Params: params})
	case <-l.ctx.Done():
	}
	return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
}

func (l *lane) send(value frame) {
	l.sendFrame(value, false)
}

func (l *lane) sendFrame(value frame, closeAfterSend bool) {
	value.JSONRPC = "2.0"
	encoded, err := json.Marshal(value)
	if err != nil {
		_ = l.conn.Close()
		return
	}
	l.enqueue(outbound{raw: encoded, closeAfterSend: closeAfterSend})
}

func (l *lane) sendRaw(raw []byte) {
	l.enqueue(outbound{raw: raw})
}

func (l *lane) enqueue(output outbound) {
	size := int64(len(output.raw))
	if l.queuedBytes.Add(size) <= 4<<20 {
		select {
		case l.out <- output:
			return
		case <-l.ctx.Done():
		default:
		}
	}
	l.queuedBytes.Add(-size)
	l.cancel()
}

func (l *lane) writeOutput() {
	for {
		select {
		case output := <-l.out:
			_ = l.conn.SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
			buffers := net.Buffers{output.raw, []byte{'\n'}}
			_, err := buffers.WriteTo(l.conn)
			l.queuedBytes.Add(-int64(len(output.raw)))
			if err != nil || output.closeAfterSend {
				l.cancel()
				return
			}
		case <-l.ctx.Done():
			return
		}
	}
}

func (l *lane) close() {
	l.cancel()
	l.mu.Lock()
	id := l.sessionID
	closed := l.nativeClosed
	l.mu.Unlock()
	p := l.peer
	p.mu.Lock()
	delete(p.sessions, id)
	p.lanes--
	idle := p.lanes == 0
	p.mu.Unlock()
	if idle {
		time.AfterFunc(30*time.Second, p.retireIdle)
	}
	if id == "" || closed || p.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, time.Second)
	defer cancel()
	_, err := p.request(ctx, "session/close", mustJSON(map[string]string{"sessionId": id}))
	if err != nil {
		// A peer whose session resources could not be released must not linger
		// idle. Other active lanes stay attached until they finish.
		p.retireIdle()
	}
}

func mustJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
