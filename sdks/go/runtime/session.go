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
	setup       []setupUpdate
	setupBytes  int
	setupGap    bool
}

type setupUpdate struct {
	sessionID string
	body      json.RawMessage
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
	blocks, preview, err := promptBlocks(q, s.info.Capabilities)
	if err != nil {
		return nil, err
	}
	t := &turn{id: q.OperationId, written: s.p.readyPrompt(), done: make(chan struct{})}
	s.turn = t
	s.emit(&model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: model.Speaker_SPEAKER_USER, Text: preview}}})
	receipt := s.record(q.OperationId, q, model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, "turn accepted")
	go s.prompt(t, blocks)
	return receipt, nil
}
func (s *session) prompt(t *turn, blocks []map[string]string) {
	params := map[string]any{"sessionId": s.info.Ref.SessionId, "prompt": blocks}
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
		if s.info == nil && s.setup != nil {
			// Retain session/new notifications until the response verifies their
			// session ID. Resume/load replay is handled by the passive reader.
			if len(s.setup) >= 32 || s.setupBytes+len(msg.Update) > 1<<20 {
				s.setupGap = true
			} else {
				s.setup = append(s.setup, setupUpdate{msg.SessionID, msg.Update})
				s.setupBytes += len(msg.Update)
			}
		} else if s.info != nil && msg.SessionID == s.info.Ref.SessionId && !s.closed {
			s.acceptUpdate(msg.Update)
		}
		return nil, nil
	case "session/request_permission":
		return s.requestPermission(ctx, raw)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (s *session) acceptUpdate(body json.RawMessage) {
	observation := normalizeUpdate(body)
	if observation == nil {
		return
	}
	s.emit(observation)
	if capture := s.p.runtime.config.OnACPUpdate; capture != nil {
		ref := &model.SessionRef{HarnessInstance: s.info.Ref.HarnessInstance, SessionId: s.info.Ref.SessionId}
		update := ACPUpdate{Session: ref, TurnID: observation.TurnId, Cursor: observation.Cursor, Body: append(json.RawMessage(nil), body...)}
		if observation.TurnId == "" && observation.GetSessionUpdate() != nil {
			update.SessionUpdate = proto.Clone(observation.GetSessionUpdate()).(*model.SessionUpdate)
		}
		capture(update)
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
		SessionUpdate                         string
		Content, RawOutput                    json.RawMessage
		ToolCallID, Title, Name, Status, Kind string
	}
	if json.Unmarshal(raw, &u) != nil || u.SessionUpdate == "" {
		return nil
	}
	switch u.SessionUpdate {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		speaker := model.Speaker_SPEAKER_ASSISTANT
		if u.SessionUpdate == "user_message_chunk" {
			speaker = model.Speaker_SPEAKER_USER
		}
		if observation := messageContent(u.Content, speaker, u.SessionUpdate == "agent_thought_chunk"); observation != nil {
			return observation
		}
	case "tool_call", "tool_call_update":
		phase := u.Status
		summary, failed := toolSummary(u.Content, u.RawOutput, u.Kind)
		if failed && (phase == "" || phase == "completed") {
			phase = "failed"
		}
		if phase == "" {
			phase = u.SessionUpdate
		}
		name := u.Title
		if name == "" {
			name = u.Name
		}
		return &model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{CallId: u.ToolCallID, Name: name, Phase: phase, Summary: summary}}}
	case "plan", "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update":
		if change := sessionChange(raw); change != nil {
			return &model.Observation{Event: &model.Observation_SessionUpdate{SessionUpdate: change}}
		}
	}
	return &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{Type: u.SessionUpdate, MediaType: "application/json", Data: raw}}}
}

type toolContentBlock struct {
	Type, Path, TerminalID string
	Content                json.RawMessage
}

type mcpToolResult struct {
	Content           []json.RawMessage
	StructuredContent json.RawMessage
	IsError           bool
}

// toolSummary keeps ACP display content and takes failure evidence from either
// a direct MCP result or a JSON-RPC reply carried by the adapter in rawOutput.
func toolSummary(raw, rawOutput json.RawMessage, kind string) (string, bool) {
	var blocks []toolContentBlock
	var summary string
	if json.Unmarshal(raw, &blocks) == nil {
		summary = formatToolBlocks(blocks)
	}
	var output struct {
		mcpToolResult
		Result *mcpToolResult
		Error  *struct {
			Code    *int
			Message string
		}
	}
	var prefix []string
	var failed bool
	if json.Unmarshal(rawOutput, &output) == nil {
		result := output.Result
		if result == nil {
			result = &output.mcpToolResult
		}
		failed = result.IsError
		if output.Error != nil {
			failed = failed || output.Error.Code != nil
			if output.Error.Message != "" && (summary == "" || output.Error.Code != nil) {
				prefix = append(prefix, output.Error.Message)
			}
		}
		if len(result.StructuredContent) != 0 && result.StructuredContent[0] == '{' {
			prefix = append(prefix, "[Structured MCP result]")
		}
		if summary == "" {
			blocks = make([]toolContentBlock, 0, len(result.Content))
			for _, part := range result.Content {
				blocks = append(blocks, toolContentBlock{Type: "content", Content: part})
			}
			summary = formatToolBlocks(blocks)
		}
	}
	if summary != "" {
		prefix = append(prefix, summary)
	}
	if len(prefix) == 0 {
		return kind, failed
	}
	return boundedToolText(strings.Join(prefix, "\n")), failed
}

func boundedToolText(text string) string {
	const runeLimit = 1024
	count := 0
	for i := range text {
		if count == runeLimit {
			return text[:i] + "…"
		}
		count++
	}
	return text
}

func formatToolBlocks(blocks []toolContentBlock) string {
	const runeLimit = 1024
	var output strings.Builder
	count := 0
	for _, block := range blocks {
		var text string
		switch block.Type {
		case "content":
			text = toolContentPreview(block.Content)
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
	return output.String()
}

// toolContentPreview renders textual content and reference descriptors without
// pulling external resources or copying embedded binary bytes into the preview.
func toolContentPreview(raw json.RawMessage) string {
	var block struct {
		Type, Text, URI, MimeType, Name, Title, Description string
		Size                                                *int64
		Resource                                            struct {
			URI, MimeType string
			Text          *string
		}
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return "[Unreadable ACP content block]"
	}
	var parts []string
	switch block.Type {
	case "text":
		return block.Text
	case "resource_link":
		parts = []string{"[Resource link]", block.Title, block.Name, block.URI, block.MimeType}
		if block.Size != nil {
			parts = append(parts, fmt.Sprintf("%d bytes", *block.Size))
		}
		if block.Description != "" {
			parts = append(parts, "\n"+block.Description)
		}
	case "resource":
		parts = []string{"[Embedded resource]", block.Resource.URI, block.Resource.MimeType}
		if block.Resource.Text != nil {
			return joinPreviewParts(parts) + "\n" + *block.Resource.Text
		}
	case "image", "audio":
		parts = []string{"[ACP " + block.Type + "]", block.MimeType, block.URI}
	case "":
		return "[ACP content type missing]"
	default:
		return "[ACP content: " + block.Type + "]"
	}
	return joinPreviewParts(parts)
}

func joinPreviewParts(parts []string) string {
	var present []string
	for _, part := range parts {
		if part != "" {
			present = append(present, part)
		}
	}
	return strings.Join(present, " ")
}
