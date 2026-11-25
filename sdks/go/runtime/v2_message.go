package runtime

import (
	"encoding/json"
	"strings"

	model "agent-acp.local/sdks/go/model"
)

// ACPV2MessageStateSource contains the bounded preview after a whole-message
// update. Its separate source record preserves the exact peer patch.
const ACPV2MessageStateSource = "acp.v2_message_state"

const (
	v2MessageCount = 64
	v2MessageBytes = 32 << 10
)

type v2MessageKey struct {
	kind, id string
}

type v2Message struct {
	Kind    string                     `json:"kind"`
	ID      string                     `json:"messageId"`
	Content []json.RawMessage          `json:"content"`
	Meta    map[string]json.RawMessage `json:"_meta,omitempty"`
	Omitted bool                       `json:"omitted,omitempty"`
	bytes   int
}

func (v *v2Session) updateMessage(kind, id string, content, meta json.RawMessage, fallback *model.Observation) *model.Observation {
	role, chunk := strings.CutSuffix(kind, "_chunk")
	switch role {
	case "user_message", "agent_message", "agent_thought", "session_message":
	default:
		return fallback
	}
	if len(id) > 4096 {
		return fallback
	}
	if v.messages == nil {
		v.messages = make(map[v2MessageKey]*v2Message)
	}
	key := v2MessageKey{role, id}
	message := v.messages[key]
	if message == nil {
		if len(v.msgOrder) == v2MessageCount {
			delete(v.messages, v.msgOrder[0])
			copy(v.msgOrder, v.msgOrder[1:])
			v.msgOrder = v.msgOrder[:len(v.msgOrder)-1]
		}
		message = &v2Message{Kind: role, ID: id, Content: []json.RawMessage{}}
		v.msgOrder = append(v.msgOrder, key)
		v.messages[key] = message
	}
	if chunk {
		if len(content) > 0 && !message.Omitted {
			if len(content)+message.bytes > v2MessageBytes || len(message.Content) >= liveWindow {
				message.omit()
			} else {
				message.Content = append(message.Content, content)
				message.bytes += len(content)
			}
		}
		return fallback
	}

	if len(content) > 0 {
		var blocks []json.RawMessage
		if len(content) > v2MessageBytes || json.Unmarshal(content, &blocks) != nil || len(blocks) > liveWindow {
			message.omit()
		} else {
			message.Content = blocks
			if message.Content == nil {
				message.Content = []json.RawMessage{}
			}
			message.Omitted = false
		}
	}
	if string(meta) == "null" {
		message.Meta = nil
	} else if len(meta) > 0 {
		if len(meta) > v2MessageBytes {
			message.omit()
			message.Meta = nil
		} else {
			var patch map[string]json.RawMessage
			if json.Unmarshal(meta, &patch) == nil {
				if message.Meta == nil {
					message.Meta = make(map[string]json.RawMessage)
				}
				for key, value := range patch {
					if string(value) == "null" {
						delete(message.Meta, key)
					} else {
						message.Meta[key] = value
					}
				}
			}
		}
	}
	encoded, _ := json.Marshal(message)
	if len(encoded) > v2MessageBytes {
		message.omit()
		message.Meta = nil
		encoded, _ = json.Marshal(message)
	}
	message.bytes = len(encoded)
	return &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{
		Type: ACPV2MessageStateSource, MediaType: "application/json", Data: encoded,
	}}}
}

func (m *v2Message) omit() {
	m.Content, m.Omitted = nil, true
}
