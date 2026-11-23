package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	model "agent-acp.local/sdks/go/model"
	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/protobuf/proto"
)

type session struct {
	cwd         string
	mu          sync.Mutex
	p           *peer
	info        *model.Session
	turn        *turn
	permissions map[string]*permission
	operations  map[string]operation
	closed      bool
	epoch       string
	sequence    uint64
	windowBytes int
	events      []*model.Observation
	changed     chan struct{}
}

type turn struct {
	id      string
	written <-chan struct{}
	done    chan struct{}
}
type permission struct {
	options map[string]bool
	answer  chan string
}
type operation struct {
	digest  [32]byte
	receipt *model.Receipt
}

func identifier() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func newSession() *session {
	return &session{permissions: map[string]*permission{}, operations: map[string]operation{}, changed: make(chan struct{}), epoch: identifier()}
}

func (s *session) existing(id string, req proto.Message) (*model.Receipt, error) {
	if id == "" {
		return nil, errors.New("operation ID is required")
	}
	if op, ok := s.operations[id]; ok {
		if fingerprint(req) != op.digest {
			return nil, errors.New("operation ID was used with different parameters")
		}
		return proto.Clone(op.receipt).(*model.Receipt), nil
	}
	if s.closed || s.p.disconnected() {
		return nil, errors.New("adapter is disconnected")
	}
	return nil, nil
}
func fingerprint(req proto.Message) [32]byte {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	return sha256.Sum256(append([]byte(string(req.ProtoReflect().Descriptor().FullName())+"\x00"), b...))
}
func (s *session) record(id string, req proto.Message, status model.DeliveryStatus, detail string) *model.Receipt {
	receipt := &model.Receipt{OperationId: id, Status: status, Detail: detail}
	s.operations[id] = operation{fingerprint(req), receipt}
	return proto.Clone(receipt).(*model.Receipt)
}

func (s *session) send(q *model.SendRequest) (*model.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := s.existing(q.OperationId, q); v != nil || err != nil {
		return v, err
	}
	if s.turn != nil {
		return nil, errors.New("session already has an active turn")
	}
	t := &turn{id: q.OperationId, written: s.p.readyPrompt(), done: make(chan struct{})}
	s.turn = t
	s.emit(&model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: model.Speaker_SPEAKER_USER, Text: q.Text}}})
	receipt := s.record(q.OperationId, q, model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, "turn accepted")
	go s.prompt(t, q.Text)
	return receipt, nil
}
func (s *session) prompt(t *turn, text string) {
	params := map[string]any{"sessionId": s.info.Ref.SessionId, "prompt": []map[string]string{{"type": "text", "text": text}}}
	result, err := rpc[struct{ StopReason string }](s.p.runtime.ctx, s.p, "session/prompt", params)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != t {
		return
	}
	if err != nil {
		s.unknown(t.id)
		s.end(err.Error())
		return
	}
	op := s.operations[t.id]
	op.receipt.Status = model.DeliveryStatus_DELIVERY_STATUS_DELIVERED
	op.receipt.Detail = "adapter completed turn"
	s.end(result.StopReason)
}

func (s *session) unknown(id string) {
	op := s.operations[id]
	op.receipt.Status = model.DeliveryStatus_DELIVERY_STATUS_UNKNOWN
	op.receipt.Detail = "provider delivery could not be confirmed"
}
func (s *session) end(reason string) {
	s.emit(&model.Observation{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: reason}}})
	close(s.turn.done)
	s.turn = nil
	clear(s.permissions)
}
func (s *session) disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != nil {
		s.unknown(s.turn.id)
		s.end("adapter disconnected")
	}
	s.closed = true
	s.signal()
}

func (s *session) answer(q *model.AnswerRequest) (*model.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := s.existing(q.OperationId, q); v != nil || err != nil {
		return v, err
	}
	pending := s.permissions[q.RequestId]
	if pending == nil {
		return nil, errors.New("request is not pending in this session")
	}
	selected, ok := q.Answer.(*model.AnswerRequest_OptionId)
	if !ok || !pending.options[selected.OptionId] {
		return nil, errors.New("select one of the pending request's option IDs")
	}
	delete(s.permissions, q.RequestId)
	pending.answer <- selected.OptionId
	return s.record(q.OperationId, q, model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, "permission decision accepted for reply"), nil
}

func (s *session) interrupt(ctx context.Context, q *model.InterruptRequest) (*model.Receipt, error) {
	s.mu.Lock()
	if v, err := s.existing(q.OperationId, q); v != nil || err != nil {
		s.mu.Unlock()
		return v, err
	}
	t := s.turn
	if t == nil || t.id != q.TurnId {
		s.mu.Unlock()
		return nil, errors.New("target turn is not active")
	}
	s.mu.Unlock()
	// A cancel has only a session ID on the wire. Order it behind the prompt
	// and recheck the original turn while holding the lock used by Send.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, errors.New("target turn ended")
	case <-t.written:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := s.existing(q.OperationId, q); v != nil || err != nil {
		return v, err
	}
	if s.turn != t {
		return nil, errors.New("target turn ended")
	}
	err := s.p.conn.SendNotification(ctx, "session/cancel", map[string]string{"sessionId": s.info.Ref.SessionId})
	if err != nil {
		return s.record(q.OperationId, q, model.DeliveryStatus_DELIVERY_STATUS_UNKNOWN, err.Error()), nil
	}
	return s.record(q.OperationId, q, model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, "cancellation sent to adapter"), nil
}

func (s *session) handle(ctx context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "session/update":
		var msg struct {
			SessionID string
			Update    json.RawMessage
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, acp.NewInvalidParams(err.Error())
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// Initial and resume/load replay notifications arrive before the session
		// becomes live. Historical decisions are never registered as actionable.
		if s.info != nil && msg.SessionID == s.info.Ref.SessionId && !s.closed {
			if obs := normalizeUpdate(msg.Update); obs != nil {
				s.emit(obs)
			}
		}
		return nil, nil
	case "session/request_permission":
		return s.requestPermission(ctx, raw)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (s *session) requestPermission(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	var msg struct {
		SessionID string
		ToolCall  struct{ Title, ToolCallID string }
		Options   []struct{ OptionID, Name, Kind string }
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, acp.NewInvalidParams(err.Error())
	}
	cancel := map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	s.mu.Lock()
	if s.info == nil || s.closed || s.info.Ref.SessionId != msg.SessionID || s.turn == nil {
		s.mu.Unlock()
		return cancel, nil
	}
	t := s.turn
	id := identifier()
	pending := &permission{options: map[string]bool{}, answer: make(chan string, 1)}
	req := &model.InputRequest{RequestId: id, Prompt: msg.ToolCall.Title}
	if req.Prompt == "" {
		req.Prompt = fmt.Sprintf("Allow %s?", msg.ToolCall.ToolCallID)
	}
	for _, opt := range msg.Options {
		if opt.OptionID == "" || (s.p.runtime.config.OncePermissionsOnly && opt.Kind != "allow_once" && opt.Kind != "reject_once") {
			continue
		}
		pending.options[opt.OptionID] = true
		req.Choices = append(req.Choices, &model.Choice{Id: opt.OptionID, Label: opt.Name})
	}
	if len(req.Choices) == 0 {
		s.mu.Unlock()
		return cancel, nil
	}
	s.permissions[id] = pending
	s.emit(&model.Observation{Event: &model.Observation_Input{Input: req}})
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.permissions, id); s.mu.Unlock() }()
	select {
	case selected := <-pending.answer:
		return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": selected}}, nil
	case <-ctx.Done():
	case <-t.done:
	case <-s.p.runtime.ctx.Done():
	}
	return cancel, nil
}

func normalizeUpdate(raw json.RawMessage) *model.Observation {
	var u struct {
		SessionUpdate                   string
		Content                         json.RawMessage
		ToolCallID, Title, Status, Kind string
	}
	if json.Unmarshal(raw, &u) != nil || u.SessionUpdate == "" {
		return nil
	}
	switch u.SessionUpdate {
	case "agent_message_chunk", "user_message_chunk":
		var part struct{ Type, Text string }
		if json.Unmarshal(u.Content, &part) == nil && part.Type == "text" {
			speaker := model.Speaker_SPEAKER_ASSISTANT
			if u.SessionUpdate == "user_message_chunk" {
				speaker = model.Speaker_SPEAKER_USER
			}
			return &model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: speaker, Text: part.Text}}}
		}
	case "tool_call", "tool_call_update":
		phase := u.Status
		if phase == "" {
			phase = u.SessionUpdate
		}
		return &model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{CallId: u.ToolCallID, Name: u.Title, Phase: phase, Summary: toolSummary(u.Content, u.Kind)}}}
	}
	return &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{Type: u.SessionUpdate, MediaType: "application/json", Data: raw}}}
}

// toolSummary takes the displayed result from ACP's typed content blocks.
// Large output stays in the harness's native transcript.
func toolSummary(raw json.RawMessage, kind string) string {
	var blocks []struct {
		Type, Path, TerminalID string
		Content                struct{ Type, Text string }
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return kind
	}
	const runeLimit = 1024
	var output strings.Builder
	count := 0
	for _, block := range blocks {
		var text string
		switch block.Type {
		case "content":
			if block.Content.Type == "text" {
				text = block.Content.Text
			}
		case "diff":
			if block.Path != "" {
				text = "Diff " + block.Path
			}
		case "terminal":
			if block.TerminalID != "" {
				text = "Terminal " + block.TerminalID
			}
		}
		if text == "" {
			continue
		}
		if count > 0 {
			if count == runeLimit {
				return output.String() + "…"
			}
			output.WriteByte('\n')
			count++
		}
		for _, ch := range text {
			if count == runeLimit {
				return output.String() + "…"
			}
			output.WriteRune(ch)
			count++
		}
	}
	if output.Len() == 0 {
		return kind
	}
	return output.String()
}
