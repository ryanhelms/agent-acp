package runtime

import (
	"encoding/json"
	"testing"

	model "agent-acp.local/sdks/go/model"
)

func TestNormalizeUpdateTypedSessionChanges(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		check     func(*model.SessionUpdate) bool
	}{
		{"plan", `{"sessionUpdate":"plan","entries":[{"content":"Write tests","priority":"high","status":"pending"}]}`,
			func(u *model.SessionUpdate) bool {
				e := u.GetPlan().GetEntries()
				return len(e) == 1 && e[0].Content == "Write tests" && e[0].Priority == "high" && e[0].Status == "pending"
			}},
		{"commands", `{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review","input":{"hint":"path"}}]}`,
			func(u *model.SessionUpdate) bool {
				c := u.GetCommands().GetCommands()
				return len(c) == 1 && c[0].Name == "review" && c[0].GetInputHint() == "path"
			}},
		{"mode", `{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
			func(u *model.SessionUpdate) bool { return u.GetMode().GetModeId() == "plan" }},
		{"config", `{"sessionUpdate":"config_option_update","configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"opus","options":[{"value":"opus","name":"Opus"}]}]}`,
			func(u *model.SessionUpdate) bool {
				o := u.GetConfiguration().GetOptions()
				return len(o) == 1 && o[0].Kind == "select" && o[0].GetSelected() == "opus" && len(o[0].Choices) == 1
			}},
		{"info", `{"sessionUpdate":"session_info_update","title":"Port","updatedAt":null}`,
			func(u *model.SessionUpdate) bool {
				i := u.GetInfo()
				return i.GetTitle() == "Port" && i.UpdatedAt == nil && i.ClearUpdatedAt && !i.ClearTitle
			}},
		{"usage", `{"sessionUpdate":"usage_update","used":10,"size":200,"cost":{"amount":0.5,"currency":"USD"}}`,
			func(u *model.SessionUpdate) bool {
				g := u.GetUsage()
				return g.Used == 10 && g.Size == 200 && g.GetCost().GetAmount() == 0.5 && g.GetCost().GetCurrency() == "USD"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := normalizeUpdate(json.RawMessage(tc.raw))
			if obs.GetSessionUpdate() == nil || !tc.check(obs.GetSessionUpdate()) {
				t.Fatalf("unexpected observation %v", obs)
			}
		})
	}
}

func TestNormalizeUpdateMessageContent(t *testing.T) {
	text := normalizeUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`))
	if text.GetText().GetText() != "hi" || text.GetText().Speaker != model.Speaker_SPEAKER_ASSISTANT {
		t.Fatalf("plain text changed shape: %v", text)
	}
	thought := normalizeUpdate(json.RawMessage(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"considering"}}`)).GetContent()
	if !thought.GetThought() || thought.Kind != "text" || thought.Text != "considering" || thought.Speaker != model.Speaker_SPEAKER_ASSISTANT {
		t.Fatalf("thought chunk: %v", thought)
	}
	image := normalizeUpdate(json.RawMessage(`{"sessionUpdate":"user_message_chunk","content":{"type":"image","mimeType":"image/png","data":"AAH/","uri":"file:///a.png"}}`)).GetContent()
	if image.Kind != "image" || image.MimeType != "image/png" || image.Uri != "file:///a.png" || image.Speaker != model.Speaker_SPEAKER_USER || image.Thought {
		t.Fatalf("image: %v", image)
	}
	resource := normalizeUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"resource","resource":{"uri":"file:///n.txt","mimeType":"text/plain","text":"body"}}}`)).GetContent()
	if resource.Kind != "resource" || resource.Uri != "file:///n.txt" || resource.Text != "body" {
		t.Fatalf("resource: %v", resource)
	}
	link := normalizeUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"resource_link","uri":"https://x","name":"x","size":12}}`)).GetContent()
	if link.Kind != "resource_link" || link.Name != "x" || link.GetSize() != 12 {
		t.Fatalf("link: %v", link)
	}
}

func TestNormalizeUpdateUnknownStaysExtension(t *testing.T) {
	for _, raw := range []string{
		`{"sessionUpdate":"vendor_future_update","x":1}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"vendor_block"}}`,
	} {
		if normalizeUpdate(json.RawMessage(raw)).GetExtension() == nil {
			t.Fatalf("%s left the extension fallback", raw)
		}
	}
}
