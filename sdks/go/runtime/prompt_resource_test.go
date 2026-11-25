package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	model "agent-acp.local/sdks/go/model"
)

func TestPromptResourceBlocks(t *testing.T) {
	link := &model.PromptPart{Content: &model.PromptPart_ResourceLink{ResourceLink: &model.PromptResourceLink{
		Uri: "file:///workspace/notes.txt", Name: "Notes", MimeType: "text/plain",
	}}}
	snapshot := &model.PromptPart{Content: &model.PromptPart_Resource{Resource: &model.PromptResource{
		Uri: "urn:commander:notes", MimeType: "text/plain",
		Body: &model.PromptResource_Text{Text: "exact $value\n"},
	}}}
	request := &model.SendRequest{Parts: []*model.PromptPart{link, snapshot}}
	if _, _, err := promptBlocks(request, &model.Capabilities{}); err == nil {
		t.Fatal("unnegotiated embedded resource was accepted")
	}
	blocks, _, err := promptBlocks(request, &model.Capabilities{CanPromptEmbeddedContext: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	const expected = `[
		{"type":"resource_link","uri":"file:///workspace/notes.txt","name":"Notes","mimeType":"text/plain"},
		{"type":"resource","resource":{"uri":"urn:commander:notes","mimeType":"text/plain","text":"exact $value\n"}}
	]`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire prompt = %s", encoded)
	}
	if blocks, _, err := promptBlocks(&model.SendRequest{Parts: []*model.PromptPart{link}}, nil); len(blocks) != 1 || err != nil {
		t.Fatalf("baseline link support: %v, %v", blocks, err)
	}
}

func TestV2ModelRequiresAuthoritativeConfigID(t *testing.T) {
	legacy := json.RawMessage(`{"id":"model","category":"model","type":"select","currentValue":"fable","options":[{"value":"fable"}]}`)
	preview := json.RawMessage(`{"configId":"model","category":"model","type":"select","currentValue":"opus","options":[{"groupId":"group","options":[{"value":"opus"}]}]}`)
	choice, err := modelOption([]json.RawMessage{legacy, preview}, "", 2)
	if err != nil || choice == nil || choice.ID != "model" || choice.CurrentValue != "opus" || !choice.offers("opus") {
		t.Fatalf("preview identifier and grouped option: %+v, %v", choice, err)
	}
}
