package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
)

const (
	permissionParams = `{"sessionId":"source-session","requestId":"vendor-field","toolCall":{"toolCallId":"tool-native","title":"Open document?","rawInput":{"path":"a.pdf"},"_meta":{"tool":"vendor"}},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once","_meta":{"hotkey":"1"}},{"optionId":"always","kind":"allow_always","name":"Always allow"}],"_meta":{"trace":9007199254740993}}`
	suppressedParams = `{"sessionId":"source-session","toolCall":{"title":"Persistent?"},"options":[{"optionId":"always","kind":"allow_always","name":"Always"}],"_meta":{"policy":"fixture"}}`
	badParams        = `{"sessionId":"source-session","options":"wrong shape","_meta":{"malformed":"fixture"}}`
	promptResult     = `{"stopReason":"end_turn","usage":{"inputTokens":4,"outputTokens":2},"_meta":{"vendor":{"limit":9007199254740993}}}`
	badResult        = `{"stopReason":false,"_meta":{"bad":true}}`
)

// Send original params with a JSON-RPC ID that is different from identifiers
// inside the provider payload and from the reply target minted by the SDK.
func TestOperationSourcePeer(t *testing.T) {
	if os.Getenv("ACP_OPERATION_SOURCE_PEER") != "1" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	write := func(message map[string]any) {
		message["jsonrpc"] = "2.0"
		if err := enc.Encode(message); err != nil {
			os.Exit(2)
		}
	}
	permission := func(wireID, raw string) {
		write(map[string]any{"method": "session/request_permission", "id": wireID, "params": json.RawMessage(raw)})
	}
	scanner := bufio.NewScanner(os.Stdin)
	var promptID json.RawMessage
	for scanner.Scan() {
		var message struct {
			ID, Params, Result, Error json.RawMessage
			Method                    string
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(3)
		}
		result := func(value any) { write(map[string]any{"id": message.ID, "result": value}) }
		switch message.Method {
		case "initialize":
			result(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "_meta": map[string]string{"setup": "private handshake marker"}})
		case "session/new":
			result(map[string]string{"sessionId": "source-session"})
		case "session/prompt":
			promptID = message.ID
			var p struct{ Prompt []struct{ Text string } }
			if json.Unmarshal(message.Params, &p) != nil || len(p.Prompt) != 1 {
				os.Exit(4)
			}
			switch p.Prompt[0].Text {
			case "permission":
				permission("foreign-wire-id", strings.Replace(permissionParams, "source-session", "different-session", 1))
			case "malformed":
				result(json.RawMessage(badResult))
			case "peer failure":
				write(map[string]any{"id": message.ID, "error": json.RawMessage(`{"code":-32077,"message":"vendor refusal","data":{"number":9007199254740993}}`)})
			case "disconnect":
				os.Exit(0)
			default:
				os.Exit(5)
			}
		case "":
			switch string(message.ID) {
			case `"foreign-wire-id"`:
				if !bytes.Contains(message.Result, []byte(`"cancelled"`)) {
					os.Exit(6)
				}
				permission("malformed-wire-id", badParams)
			case `"malformed-wire-id"`:
				if !bytes.Contains(message.Error, []byte("-32602")) {
					os.Exit(7)
				}
				permission("suppressed-wire-id", suppressedParams)
			case `"suppressed-wire-id"`:
				if !bytes.Contains(message.Result, []byte(`"cancelled"`)) {
					os.Exit(8)
				}
				permission("accepted-wire-id", permissionParams)
			case `"accepted-wire-id"`:
				if !bytes.Contains(message.Result, []byte(`"optionId":"once"`)) {
					os.Exit(9)
				}
				write(map[string]any{"id": promptID, "result": json.RawMessage(promptResult)})
			default:
				os.Exit(10)
			}
		}
	}
	os.Exit(0)
}

func TestOperationSourceScopeEvidenceAndWindow(t *testing.T) {
	r, err := New(Config{HarnessInstance: "fixture", Command: os.Args[0], Args: []string{"-test.run=^TestOperationSourcePeer$"},
		WorkingDirectory: t.TempDir(), Env: map[string]string{"ACP_OPERATION_SOURCE_PEER": "1"}, OncePermissionsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	client := agentacp.New(r)
	opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ref := opened.Session.Ref
	stream := observe(t, r, ctx, ref, "")
	send(t, client, ctx, ref, "turn-one", "permission")
	if o := recv(t, stream); o.GetText().Text != "permission" || o.SourceRecord != nil {
		t.Fatal(o)
	}
	check := func(o *model.Observation, source *model.Extension, kind, body, turn string) {
		t.Helper()
		if source.GetType() != kind || source.GetMediaType() != "application/json" || string(source.GetData()) != body ||
			o.Session.GetSessionId() != ref.SessionId || o.Session.GetHarnessInstance() != ref.HarnessInstance ||
			o.Cursor == "" || o.GetSourceCursor() != o.Cursor || o.TurnId != turn {
			t.Fatalf("uncorrelated or changed source: %v", o)
		}
	}
	for _, raw := range []string{badParams, suppressedParams} {
		o := recv(t, stream)
		check(o, o.GetExtension(), "acp.permission_request", raw, "turn-one")
		if o.GetInput() != nil || o.SourceRecord != nil {
			t.Fatal("passive request was actionable or owned the source twice")
		}
	}
	input := recv(t, stream)
	check(input, input.SourceRecord, "acp.permission_request", permissionParams, "turn-one")
	if q := input.GetInput(); q.GetRequestId() == "" || q.GetRequestId() == "accepted-wire-id" || q.GetRequestId() == "vendor-field" ||
		len(q.Choices) != 1 || q.Choices[0].Id != "once" {
		t.Fatalf("invalid permission identity or choices: %v", q)
	}
	answer := &model.AnswerRequest{Session: ref, OperationId: "answer", RequestId: input.GetInput().RequestId, Answer: &model.AnswerRequest_OptionId{OptionId: "always"}}
	if _, err := client.Answer(ctx, answer); err == nil {
		t.Fatal("source-only option was actionable")
	}
	answer.Answer = &model.AnswerRequest_OptionId{OptionId: "once"}
	if _, err := client.Answer(ctx, answer); err != nil {
		t.Fatal(err)
	}
	end := recv(t, stream)
	check(end, end.SourceRecord, "acp.prompt_result", promptResult, "turn-one")
	if end.GetTurnEnded().GetReason() != "end_turn" {
		t.Fatal(end)
	}

	for _, prompt := range []string{"malformed", "peer failure", "disconnect"} {
		op := "turn-" + prompt
		send(t, client, ctx, ref, op, prompt)
		recv(t, stream)
		o := recv(t, stream)
		if o.GetTurnEnded() == nil || o.TurnId != op {
			t.Fatal(o)
		}
		if prompt == "malformed" {
			check(o, o.SourceRecord, "acp.prompt_result", badResult, op)
		}
		if prompt == "peer failure" {
			check(o, o.SourceRecord, "acp.prompt_error", `{"code":-32077,"message":"vendor refusal","data":{"number":9007199254740993}}`, op)
			if o.GetTurnEnded().Reason != "adapter returned a prompt error" {
				t.Fatal("provider details leaked into terminal preview")
			}
		}
		if prompt == "disconnect" && (o.SourceRecord != nil || o.GetTurnEnded().Reason != "adapter disconnected") {
			t.Fatalf("incorrect terminal evidence after peer loss: %v", o)
		}
		same, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: op, Text: prompt})
		if err != nil || same.GetReceipt().GetStatus() != model.DeliveryStatus_DELIVERY_STATUS_UNKNOWN {
			t.Fatalf("uncertain delivery: %v %v", same, err)
		}
	}
}

func TestOversizedOperationSourceLeavesActionAndTerminalVisible(t *testing.T) {
	s := newSession()
	s.info = &model.Session{Ref: &model.SessionRef{SessionId: "scope", HarnessInstance: "fixture"}}
	s.turn = &turn{id: "turn"}
	raw := json.RawMessage(fmt.Sprintf(`{"large":"%s"}`, strings.Repeat("x", liveWindowBytes)))
	for _, e := range []*model.Observation{
		{Event: &model.Observation_Input{Input: &model.InputRequest{RequestId: "local"}}},
		{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: "end_turn"}}},
		nil,
	} {
		s.emitSource(e, "acp.large_fixture", raw)
		observed := s.events[len(s.events)-1]
		marker := observed.GetExtension()
		if e != nil {
			marker = observed.SourceRecord
			if e != observed {
				t.Fatal("large native data masked the semantic event")
			}
		}
		if marker.GetType() != "acp.source_omitted" || observed.GetSourceCursor() != observed.Cursor ||
			observed.GetGap() != nil || observed.TurnId != "turn" || s.windowBytes > liveWindowBytes ||
			string(marker.Data) != fmt.Sprintf(`{"sourceType":"acp.large_fixture","reason":"live_window_limit","byteCount":%d}`, len(raw)) {
			t.Fatal("large native data wasn't reported as a source-only omission")
		}
	}
}
