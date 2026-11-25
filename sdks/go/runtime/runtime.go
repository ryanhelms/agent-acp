// Package runtime connects the portable sessions API to a local agent process.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

// MCPServer registers a stdio MCP server for each created or resumed session.
type MCPServer struct {
	Name, Command string
	Args          []string
	Env           map[string]string
}

// ACPUpdate is an original ACP session/update paired with its portable observation.
type ACPUpdate struct {
	Session *model.SessionRef
	TurnID  string
	Cursor  string
	Body    json.RawMessage
	// Params is the complete original params object when a session/update
	// supplies additional params or wire envelope fields. Body still addresses its update
	// member, including an explicit null; both share callback-owned storage.
	Params json.RawMessage
	// ParamsSize is zero for ordinary updates. When a full sourced record
	// exceeds the observation window, ParamsSize reports its original byte
	// count, Params is nil, and the observation carries acp.source_omitted.
	ParamsSize int
	// Metadata of the same selected frame, owned by the callback.
	WireProvenance *model.WireProvenance
}

// Config configures one harness instance. New starts processes on demand.
// Command can be omitted for an instance that only serves native history.
type Config struct {
	HarnessInstance  string
	Command          string
	Args             []string
	WorkingDirectory string
	Env              map[string]string
	MCPServers       []MCPServer
	Stderr           io.Writer
	// OncePermissionsOnly excludes choices that grant or deny future requests.
	OncePermissionsOnly bool
	// SessionMeta supplies optional adapter setup metadata on creation and resume.
	SessionMeta map[string]any
	// ReadHistory reads native retained records without opening a live session.
	ReadHistory func(context.Context, *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error)
	// ReplayHistory allows ReadHistory to load an inactive session in a
	// disposable ACP process when a native ReadHistory function is absent.
	ReplayHistory bool
	// OnACPUpdate optionally hands accepted live updates to an application-owned
	// source store. The callback runs in observation order and must return
	// promptly without invoking the runtime. It owns the update body and ref.
	OnACPUpdate func(ACPUpdate)
}

type Runtime struct {
	config   Config
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	resuming map[string]chan struct{}
	sessions map[string]*session
	peers    map[*peer]bool
}

var _ agentacp.Runtime = (*Runtime)(nil)

func New(config Config) (*Runtime, error) {
	if config.HarnessInstance == "" || (config.Command == "" && config.ReadHistory == nil) {
		return nil, errors.New("a harness instance and an adapter command or native history reader are required")
	}
	if config.WorkingDirectory == "" {
		var err error
		config.WorkingDirectory, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(config.WorkingDirectory) {
		return nil, errors.New("working directory must be absolute")
	}
	for _, server := range config.MCPServers {
		if server.Name == "" || server.Command == "" {
			return nil, errors.New("MCP server name and command are required")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		config: config, ctx: ctx, cancel: cancel,
		resuming: make(map[string]chan struct{}),
		sessions: make(map[string]*session),
		peers:    make(map[*peer]bool),
	}, nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	r.cancel()
	peers := make([]*peer, 0, len(r.peers))
	for p := range r.peers {
		peers = append(peers, p)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func() { defer wg.Done(); p.close() }()
	}
	wg.Wait()
	return nil
}

func (r *Runtime) validate(ref *model.SessionRef) error {
	if ref.GetHarnessInstance() != r.config.HarnessInstance || ref.GetSessionId() == "" {
		return errors.New("invalid session reference for this harness instance")
	}
	return nil
}

func (r *Runtime) session(ref *model.SessionRef) (*session, error) {
	if err := r.validate(ref); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[ref.SessionId]
	if s == nil {
		return nil, errors.New("session is not open")
	}
	return s, nil
}

func (r *Runtime) Unary(ctx context.Context, op string, req, dst proto.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ctx.Err(); err != nil {
		return errors.New("runtime is closed")
	}
	if req == nil || dst == nil || !req.ProtoReflect().IsValid() || !dst.ProtoReflect().IsValid() || string(req.ProtoReflect().Descriptor().Name()) != op+"Request" || string(dst.ProtoReflect().Descriptor().Name()) != op+"Response" {
		return fmt.Errorf("invalid %s request or response", op)
	}
	switch op {
	case "Open":
		q := req.(*model.OpenRequest)
		if q.HarnessInstance != r.config.HarnessInstance {
			return errors.New("unknown harness instance")
		}
		s, err := r.attach(ctx, "", q.WorkingDirectory, q.Model)
		if err == nil {
			dst.(*model.OpenResponse).Session = s
		}
		return err
	case "Resume":
		ref := req.(*model.ResumeRequest).Session
		if err := r.validate(ref); err != nil {
			return err
		}
		s, err := r.attach(ctx, ref.SessionId, "", "")
		if err == nil {
			dst.(*model.ResumeResponse).Session = s
		}
		return err
	case "ReadHistory":
		q := req.(*model.ReadHistoryRequest)
		if err := r.validate(q.Session); err != nil {
			return err
		}
		if r.config.ReadHistory == nil && !r.config.ReplayHistory {
			return errors.New("native history reader is unavailable for this harness")
		}
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.ctx, cancel)
		defer stop()
		defer cancel()
		read := r.config.ReadHistory
		if read == nil {
			read = r.replayHistory
		}
		page, err := read(ctx, q)
		if err != nil {
			return err
		}
		if page == nil {
			return errors.New("native history reader returned an empty response")
		}
		proto.Merge(dst, page)
		return nil
	case "ListSessions":
		q := req.(*model.ListSessionsRequest)
		if q.HarnessInstance != r.config.HarnessInstance {
			return errors.New("unknown harness instance")
		}
		return r.list(ctx, q, dst.(*model.ListSessionsResponse))
	case "Send":
		q := req.(*model.SendRequest)
		s, err := r.session(q.Session)
		if err != nil {
			return err
		}
		receipt, err := s.send(q)
		dst.(*model.SendResponse).Receipt = receipt
		return err
	case "Answer":
		q := req.(*model.AnswerRequest)
		s, err := r.session(q.Session)
		if err != nil {
			return err
		}
		receipt, err := s.answer(q)
		dst.(*model.AnswerResponse).Receipt = receipt
		return err
	case "Interrupt":
		q := req.(*model.InterruptRequest)
		s, err := r.session(q.Session)
		if err != nil {
			return err
		}
		receipt, err := s.interrupt(ctx, q)
		dst.(*model.InterruptResponse).Receipt = receipt
		return err
	default:
		return fmt.Errorf("unsupported operation %q", op)
	}
}

func (r *Runtime) attach(ctx context.Context, id, cwd, modelID string) (*model.Session, error) {
	if cwd == "" {
		cwd = r.config.WorkingDirectory
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("working directory must be absolute")
	}
	if id != "" {
		// Two callers resuming one native session must not concurrently launch it twice.
		for {
			r.mu.Lock()
			if s := r.sessions[id]; s != nil && !s.p.disconnected() {
				r.mu.Unlock()
				return proto.Clone(s.info).(*model.Session), nil
			}
			if s := r.sessions[id]; s != nil {
				cwd = s.cwd
			}
			ready := r.resuming[id]
			if ready == nil {
				ready = make(chan struct{})
				r.resuming[id] = ready
				r.mu.Unlock()
				defer func() {
					r.mu.Lock()
					delete(r.resuming, id)
					close(ready)
					r.mu.Unlock()
				}()
				break
			}
			r.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-r.ctx.Done():
				return nil, errors.New("runtime is closed")
			}
		}
	}
	s := newSession()
	p, init, err := r.connect(ctx, s.handle, s.selectWire)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			p.close()
		}
	}()
	caps := r.capabilities(init)
	params := map[string]any{"cwd": cwd, "mcpServers": r.mcpServers()}
	if r.config.SessionMeta != nil {
		params["_meta"] = r.config.SessionMeta
	}
	if id == "" {
		resp, err := rpc[struct {
			SessionID     string
			ConfigOptions []struct{ ID, Category, Type string }
		}](ctx, p, "session/new", params)
		if err != nil {
			return nil, err
		}
		id = resp.SessionID
		if id == "" {
			return nil, errors.New("adapter returned an empty session ID")
		}
		if modelID != "" {
			method := "session/set_model"
			values := map[string]any{"sessionId": id, "modelId": modelID}
			for _, option := range resp.ConfigOptions {
				if option.ID != "" && option.Category == "model" && option.Type == "select" {
					method = "session/set_config_option"
					values = map[string]any{"sessionId": id, "configId": option.ID, "value": modelID}
					break
				}
			}
			if _, err := rpc[empty](ctx, p, method, values); err != nil {
				return nil, fmt.Errorf("select model: %w", err)
			}
		}
	} else {
		if !caps.CanResume {
			return nil, errors.New("adapter does not support resuming sessions")
		}
		method := "session/load"
		if init.AgentCapabilities.SessionCapabilities.Resume != nil {
			method = "session/resume"
		}
		params["sessionId"] = id
		if _, err := rpc[empty](ctx, p, method, params); err != nil {
			return nil, err
		}
	}
	info := &model.Session{Ref: &model.SessionRef{HarnessInstance: r.config.HarnessInstance, SessionId: id}, Capabilities: caps, DisplayName: init.AgentInfo.Name}
	s.mu.Lock()
	s.p = p
	s.cwd = cwd
	s.info = info
	s.wireSession.Store(&info.Ref.SessionId)
	s.mu.Unlock()
	r.mu.Lock()
	current := r.sessions[id]
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil, errors.New("runtime is closed")
	}
	if current != nil && !current.p.disconnected() {
		r.mu.Unlock()
		return proto.Clone(current.info).(*model.Session), nil
	}
	if current != nil {
		current.mu.Lock()
		s.mu.Lock()
		for opID, op := range current.operations {
			op.receipt = proto.Clone(op.receipt).(*model.Receipt)
			if current.turn != nil && current.turn.id == opID {
				op.receipt.Status = model.DeliveryStatus_DELIVERY_STATUS_UNKNOWN
				op.receipt.Detail = "adapter disconnected before confirming delivery"
			}
			s.operations[opID] = op
		}
		s.mu.Unlock()
		current.mu.Unlock()
	}
	r.sessions[id] = s
	owned = true
	r.mu.Unlock()
	go func() {
		<-p.conn.Done()
		s.disconnect()
		p.close()
	}()
	return proto.Clone(info).(*model.Session), nil
}
