package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
)

func TestV2Peer(t *testing.T) {
	scenario := os.Getenv("ACP_V2_PEER")
	if scenario == "" {
		return
	}
	out := json.NewEncoder(os.Stdout)
	write := func(row map[string]any) {
		row["jsonrpc"] = "2.0"
		if out.Encode(row) != nil {
			os.Exit(1)
		}
	}
	send := func(id json.RawMessage, result any) { write(map[string]any{"id": id, "result": result}) }
	update := func(id string, body map[string]any) {
		write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": id, "update": body}})
	}
	state := func(status, reason string) {
		body := map[string]any{"sessionUpdate": "state_update", "state": status, "background": []map[string]string{{"type": "tool_call", "toolCallId": "bg"}}}
		if reason != "" {
			body["stopReason"] = reason
		}
		update("session", body)
	}
	request := func(id int, title, tool string) {
		write(map[string]any{"id": id, "method": "session/request_permission", "params": map[string]any{
			"sessionId": "session", "title": title,
			"subject": map[string]any{"type": "tool_call", "toolCall": map[string]string{"toolCallId": tool, "title": title, "status": "pending"}},
			"options": []map[string]string{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}})
	}
	textUpdate := func(session, value string) {
		update(session, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "asst", "content": map[string]string{
			"type": "text", "text": value,
		}})
	}
	scan := bufio.NewScanner(os.Stdin)
	negotiated := 0
	prompted := false
	for scan.Scan() {
		var req struct {
			ID     json.RawMessage
			Method string
			Params map[string]any
			Result struct {
				Outcome struct{ Outcome, OptionID string }
			}
		}
		if json.Unmarshal(scan.Bytes(), &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			if negotiated == 0 && req.Params["protocolVersion"] == float64(2) && req.Params["capabilities"] != nil && req.Params["info"] != nil {
				if scenario == "fallback" {
					negotiated = 1
					send(req.ID, map[string]int{"protocolVersion": 1})
					continue
				}
				negotiated = 2
				send(req.ID, map[string]any{"protocolVersion": 2, "capabilities": map[string]any{"session": map[string]any{}}, "info": map[string]string{"name": "v2 fixture"}})
			} else if negotiated == 1 && scenario == "fallback" && req.Params["protocolVersion"] == float64(1) && req.Params["clientCapabilities"] != nil {
				send(req.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "v1 fixture"}})
			} else {
				os.Exit(3)
			}
		case "session/new":
			send(req.ID, map[string]string{"sessionId": "session"})
		case "session/prompt":
			if prompted || req.Params["sessionId"] != "session" {
				os.Exit(4)
			}
			prompted = true
			if negotiated == 1 {
				if req.Params["idempotencyKey"] != nil {
					os.Exit(5)
				}
				send(req.ID, map[string]string{"stopReason": "end_turn"})
				continue
			}
			if req.Params["idempotencyKey"] != "activity" {
				os.Exit(6)
			}
			switch scenario {
			case "cancel":
				state("running", "")
				send(req.ID, map[string]string{"messageId": "user"})
				state("requires_action", "")
				request(81, "Foreground request", "fg")
				request(82, "Background request", "bg")
			case "state-before-ack":
				state("running", "")
				textUpdate("session", "first output")
				state("idle", "end_turn")
				send(req.ID, map[string]string{"messageId": "user"})
			default:
				send(req.ID, map[string]string{"messageId": "user"})
				// An idle background snapshot cannot finish the prompt.
				state("idle", "")
				state("running", "")
				textUpdate("session", "first output")
				state("idle", "end_turn")
			}
		case "session/cancel":
			if scenario != "cancel" || req.Params["sessionId"] != "session" {
				os.Exit(7)
			}
			write(map[string]any{"method": "$/cancel_request", "params": map[string]int{"requestId": 81}})
		case "":
			switch string(req.ID) {
			case "81":
				if req.Result.Outcome.Outcome != "cancelled" {
					os.Exit(8)
				}
				state("idle", "cancelled")
			case "82":
				if req.Result.Outcome.Outcome != "selected" || req.Result.Outcome.OptionID != "allow" {
					os.Exit(9)
				}
				textUpdate("session", "background permission accepted")
			default:
				os.Exit(10)
			}
		case "session/resume":
			if cursor, ok := req.Params["replayFrom"].(map[string]any); ok {
				if cursor["type"] != "start" {
					os.Exit(11)
				}
				textUpdate("session", "retained history")
				update("crossed", map[string]any{"sessionUpdate": "replay_complete"})
				if scenario != "missing-replay-complete" {
					update("session", map[string]any{"sessionUpdate": "replay_complete"})
				}
			}
			if scenario != "missing-state" {
				state("idle", "end_turn")
			}
			send(req.ID, map[string]any{})
		case "session/close":
			send(req.ID, map[string]any{})
		default:
			os.Exit(12)
		}
	}
	os.Exit(0)
}

func v2Client(t *testing.T, scenario string) (*agentacp.Sessions, *Runtime, context.Context) {
	t.Helper()
	runtime, err := New(Config{
		HarnessInstance: "v2", WorkingDirectory: t.TempDir(), ReplayHistory: true,
		Command: os.Args[0], Args: []string{"-test.run=^TestV2Peer$"}, Env: map[string]string{"ACP_V2_PEER": scenario},
		ProtocolVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return agentacp.New(runtime), runtime, ctx
}

func TestV2NegotiationAndPromptState(t *testing.T) {
	for _, scenario := range []string{"fallback", "state-before-ack", "ack-before-state"} {
		t.Run(scenario, func(t *testing.T) {
			client, _, ctx := v2Client(t, scenario)
			opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "v2"})
			if err != nil {
				t.Fatal(err)
			}
			session := opened.GetSession()
			expectedName := "v2 fixture"
			if scenario == "fallback" {
				expectedName = "v1 fixture"
			}
			if session.GetDisplayName() != expectedName {
				t.Fatalf("unexpected negotiated session: %v", session)
			}
			ref := session.GetRef()
			if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "activity", Text: "Check foreground state"}); err != nil {
				t.Fatal(err)
			}
			var accepted, text bool
			for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
				if err != nil {
					t.Fatal(err)
				}
				if ext := event.GetExtension(); ext.GetType() == ACPV2PromptAcceptedSource {
					accepted = strings.Contains(string(ext.GetData()), `"messageId":"user"`)
				}
				if event.GetText().GetText() == "first output" {
					text = true
				}
				if end := event.GetTurnEnded(); end != nil {
					if end.Reason != "end_turn" || scenario != "fallback" && (!accepted || !text || event.GetSourceRecord().GetType() != ACPV2TurnResultSource) {
						t.Fatalf("completion missing insertion or output evidence: accepted=%v text=%v result=%v", accepted, text, event)
					}
					return
				}
			}
			t.Fatal("missing foreground completion")
		})
	}
}

func TestV2InterruptDismissesOnlyPeerCancelledPermission(t *testing.T) {
	client, _, ctx := v2Client(t, "cancel")
	opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	ref := opened.GetSession().GetRef()
	if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "activity", Text: "Check permissions"}); err != nil {
		t.Fatal(err)
	}
	var foreground, background string
	var sent, dismissed, ended bool
	answer := func(request string) error {
		_, err := client.Answer(ctx, &model.AnswerRequest{Session: ref, RequestId: request, OperationId: "answer-" + request,
			Answer: &model.AnswerRequest_OptionId{OptionId: "allow"},
		})
		return err
	}
	for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
		if err != nil {
			t.Fatal(err)
		}
		if input := event.GetInput(); input != nil {
			switch input.Prompt {
			case "Foreground request":
				foreground = input.RequestId
			case "Background request":
				background = input.RequestId
			default:
				t.Fatalf("unexpected permission: %v", input)
			}
			if !sent && foreground != "" && background != "" {
				sent = true
				if _, err := client.Interrupt(ctx, &model.InterruptRequest{Session: ref, OperationId: "interrupt", TurnId: "activity"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if ext := event.GetExtension(); ext.GetType() == ACPV2PermissionDismissedSource {
			dismissed = strings.Contains(string(ext.GetData()), foreground) && !strings.Contains(string(ext.GetData()), background)
		}
		if end := event.GetTurnEnded(); end != nil {
			ended = true
			if end.Reason != "cancelled" || !dismissed {
				t.Fatalf("wrong cancellation evidence: dismissed=%v result=%v", dismissed, end)
			}
			if err := answer(foreground); err == nil {
				t.Fatal("accepted a late foreground decision")
			}
			if err := answer(background); err != nil {
				t.Fatalf("foreground interruption discarded background permission: %v", err)
			}
		}
		if event.GetText().GetText() == "background permission accepted" && ended && event.GetTurnId() == "" {
			return
		}
	}
	t.Fatal("background work did not continue after foreground cancellation")
}

func TestV2ReplayUsesItsOwnMarkerAndCurrentState(t *testing.T) {
	for _, scenario := range []string{"replay", "missing-replay-complete", "missing-state"} {
		t.Run(scenario, func(t *testing.T) {
			client, runtime, ctx := v2Client(t, scenario)
			ref := &model.SessionRef{HarnessInstance: "v2", SessionId: "session"}
			snapshot, err := runtime.LoadReplay(ctx, ref)
			if scenario != "replay" {
				if err == nil || snapshot != nil {
					t.Fatalf("incomplete replay was published: %v %v", snapshot, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := snapshot.Page("", 10)
			if err != nil || len(page.GetObservations()) != 2 {
				t.Fatalf("replay should contain history and original source only: %v %v", page, err)
			}
			if page.Observations[0].GetText().GetText() != "retained history" {
				t.Fatalf("wrong replay history: %v", page)
			}
			if _, err := client.Resume(ctx, &model.ResumeRequest{Session: ref}); err != nil {
				t.Fatalf("resume without replay: %v", err)
			}
		})
	}
}

func TestV2MessageUpsertsReplaceAndPatchByRole(t *testing.T) {
	projection := &v2Session{}
	apply := func(kind, body, meta string) *v2Message {
		t.Helper()
		obs := projection.updateMessage(kind, "shared-id", json.RawMessage(body), json.RawMessage(meta), nil)
		if strings.HasSuffix(kind, "_chunk") {
			return nil
		}
		state := obs.GetExtension()
		if state.GetType() != ACPV2MessageStateSource {
			t.Fatalf("missing materialized message: %v", obs)
		}
		var message v2Message
		if err := json.Unmarshal(state.GetData(), &message); err != nil {
			t.Fatal(err)
		}
		return &message
	}
	apply("agent_message", `[{"type":"text","text":"first"}]`, `{"one":1,"two":2}`)
	apply("agent_message_chunk", `{"type":"text","text":"second"}`, `{"one":"ignored chunk metadata"}`)
	apply("agent_thought", `[{"type":"text","text":"private thought"}]`, "")
	current := apply("agent_message", "", `{"two":null,"three":3}`)
	if len(current.Content) != 2 || !strings.Contains(string(current.Content[1]), "second") || string(current.Meta["one"]) != "1" ||
		current.Meta["two"] != nil || string(current.Meta["three"]) != "3" {
		t.Fatalf("patch changed untouched values or failed to clear a key: %+v", current)
	}
	current = apply("agent_message", `[{"type":"text","text":"corrected"}]`, "null")
	if len(current.Content) != 1 || !strings.Contains(string(current.Content[0]), "corrected") || current.Meta != nil {
		t.Fatalf("whole-message replacement did not reset content and metadata: %+v", current)
	}
	thought := apply("agent_thought", "", "")
	if len(thought.Content) != 1 || !strings.Contains(string(thought.Content[0]), "private thought") {
		t.Fatalf("agent message crossed into its thought entity: %+v", thought)
	}
}
