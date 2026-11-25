// The v2 wire implementation follows ACP schema-v2.0.0-beta.4.
// https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v2.0.0-beta.4
package runtime

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	model "agent-acp.local/sdks/go/model"
	"agent-acp.local/transport"
)

const (
	// ACPV2PromptAcceptedSource carries the real session/prompt response.
	// Its messageId confirms insertion. A later state snapshot ends foreground work.
	ACPV2PromptAcceptedSource = "acp.v2_prompt_accepted"
	// ACPV2TurnResultSource carries the idle state notification that ended the turn.
	ACPV2TurnResultSource = "acp.v2_turn_result"
	// ACPV2PermissionDismissedSource identifies an addressed peer cancellation.
	ACPV2PermissionDismissedSource = "acp.v2_permission_dismissed"
)

type v2Session struct {
	foreground string
	stateSeen  bool
	tools      map[string]v2Tool
	toolOrder  []string
	messages   map[v2MessageKey]*v2Message
	msgOrder   []v2MessageKey
}

type v2State struct {
	reason string
	raw    json.RawMessage
	wire   *model.WireProvenance
}

type v2Turn struct {
	responseDone chan struct{}
	messageID    string
	started      bool
	terminal     *v2State
}

func (s *session) promptV2(t *turn, blocks []map[string]string) {
	defer close(t.v2.responseDone)
	_, receipt, err := transport.SendRequestWithReceipt[receiptResult](s.p.conn, s.p.runtime.ctx, "session/prompt", map[string]any{
		"sessionId": s.info.Ref.SessionId, "idempotencyKey": t.id, "prompt": blocks,
	})
	wire, result := wireRecord(receipt, "session/prompt", liveWindowBytes)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != t {
		return
	}
	if wire.GetKind() == model.WireMessageKind_WIRE_MESSAGE_KIND_ERROR {
		// A v2 error response confirms the Agent did not insert this submission.
		// Retain the exact peer error and prevent an automatic retry.
		s.unknown(t.id)
		var failure struct{ Code int }
		_ = json.Unmarshal(result, &failure)
		if failure.Code == -32800 {
			s.end("cancelled", "acp.prompt_error", result, wire)
		} else {
			s.end("adapter rejected prompt before insertion", "acp.prompt_error", result, wire)
		}
		return
	}
	var accepted struct{ MessageID string }
	if json.Unmarshal(result, &accepted) != nil || accepted.MessageID == "" {
		s.unknown(t.id)
		switch {
		case s.p.disconnected():
			s.end("adapter disconnected", "", nil, nil)
		case err != nil:
			s.end("adapter prompt delivery could not be confirmed", "", nil, nil)
		default:
			s.end("ACP v2 prompt returned an invalid messageId", ACPV2PromptAcceptedSource, result, wire)
		}
		return
	}

	op := s.operations[t.id]
	op.receipt.Status = model.DeliveryStatus_DELIVERY_STATUS_DELIVERED
	op.receipt.Detail = "adapter inserted message"
	t.v2.messageID = accepted.MessageID
	s.emitWireSource(nil, ACPV2PromptAcceptedSource, result, wire)
	s.finishV2Turn(t)
}

// updateV2State consumes only authoritative state updates. An idle snapshot
// repeated for a background-list change is not a new foreground completion.
func (s *session) updateV2State(update, raw json.RawMessage, wire *model.WireProvenance) {
	if !isV2StateUpdate(update) {
		return
	}
	var payload struct{ State, StopReason string }
	if json.Unmarshal(update, &payload) != nil {
		return
	}
	switch payload.State {
	case "idle", "running", "requires_action", "unknown":
	default:
		return
	}
	s.v2.stateSeen = true
	s.v2.foreground = payload.State
	t := s.turn
	if t == nil || t.v2.terminal != nil {
		return
	}
	switch payload.State {
	case "running", "requires_action":
		t.v2.started = true
	case "idle":
		if !t.v2.started {
			return
		}
		reason := payload.StopReason
		if reason == "" {
			reason = "unknown"
		}
		t.v2.terminal = &v2State{reason: reason, raw: raw, wire: wire}
		s.finishV2Turn(t)
	}
}

func (s *session) finishV2Turn(t *turn) {
	if terminal := t.v2.terminal; t.v2.messageID != "" && terminal != nil {
		s.end(terminal.reason, ACPV2TurnResultSource, terminal.raw, terminal.wire)
	}
}

func isV2StateUpdate(update json.RawMessage) bool {
	var header struct{ SessionUpdate string }
	return json.Unmarshal(update, &header) == nil && header.SessionUpdate == "state_update"
}

func validateV2OperationID(id string) error {
	if !utf8.ValidString(id) || utf8.RuneCountInString(id) > 255 {
		return errors.New("ACP v2 idempotency key must contain at most 255 Unicode characters")
	}
	return nil
}

func (s *session) dismissV2Permission(id string, pending *permission) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.permissions[id] != pending {
		return
	}
	delete(s.permissions, id)
	data, _ := json.Marshal(struct {
		RequestID string `json:"requestId"`
	}{id})
	s.emitWireSource(nil, ACPV2PermissionDismissedSource, data, nil)
}

type v2Tool struct {
	title, name, kind, status string
}

func (s *session) normalizeV2(raw json.RawMessage, fallback *model.Observation) *model.Observation {
	var patch struct {
		SessionUpdate, ToolCallID, MessageID string
		Title, Name, Kind, Status            json.RawMessage
		Content, RawOutput                   json.RawMessage
		Meta                                 json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &patch) != nil {
		return fallback
	}
	if patch.MessageID != "" {
		return s.v2.updateMessage(patch.SessionUpdate, patch.MessageID, patch.Content, patch.Meta, fallback)
	}
	if patch.SessionUpdate != "tool_call_update" && patch.SessionUpdate != "tool_call_content_chunk" || patch.ToolCallID == "" {
		return fallback
	}
	if len(patch.ToolCallID) > 4096 {
		// Keep oversized IDs observable via the exact source without retaining
		// them in the bounded map of active tool previews.
		return fallback
	}
	if s.v2.tools == nil {
		s.v2.tools = make(map[string]v2Tool)
	}
	tool, known := s.v2.tools[patch.ToolCallID]
	if !known {
		if len(s.v2.toolOrder) == liveWindow {
			delete(s.v2.tools, s.v2.toolOrder[0])
			copy(s.v2.toolOrder, s.v2.toolOrder[1:])
			s.v2.toolOrder = s.v2.toolOrder[:len(s.v2.toolOrder)-1]
		}
		s.v2.toolOrder = append(s.v2.toolOrder, patch.ToolCallID)
	}

	patchV2String(&tool.title, patch.Title)
	patchV2String(&tool.name, patch.Name)
	patchV2String(&tool.kind, patch.Kind)
	patchV2String(&tool.status, patch.Status)
	s.v2.tools[patch.ToolCallID] = tool
	name := tool.title
	if name == "" {
		name = tool.name
	}
	content := patch.Content
	if patch.SessionUpdate == "tool_call_content_chunk" && len(content) > 0 {
		content = append(append([]byte{'['}, content...), ']')
	}
	summary, failed := toolSummary(content, patch.RawOutput, tool.kind)
	phase := tool.status
	if failed && (phase == "" || phase == "completed") {
		phase = "failed"
	}
	if phase == "" {
		phase = patch.SessionUpdate
	}
	// A partial update must not create a second tool call just because the
	// cached tool title was unchanged.
	if known && patch.SessionUpdate == "tool_call_content_chunk" {
		name = ""
	}
	return &model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{
		CallId: patch.ToolCallID, Name: name, Phase: phase, Summary: summary,
	}}}
}

func patchV2String(dst *string, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		const maxFieldBytes = 1024
		if len(value) > maxFieldBytes {
			end := maxFieldBytes
			for !utf8.RuneStart(value[end]) {
				end--
			}
			value = value[:end]
		}
		*dst = value
	}
}

func (s *session) emitV2ToolSubject(raw json.RawMessage) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return
	}
	fields["sessionUpdate"] = json.RawMessage(`"tool_call_update"`)
	patch, err := json.Marshal(fields)
	if err != nil {
		return
	}
	if obs := s.normalizeV2(patch, nil); obs != nil {
		s.emitWireSource(obs, "acp.v2_permission_subject", raw, nil)
	}
}
