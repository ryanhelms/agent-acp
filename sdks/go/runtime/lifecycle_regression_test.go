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

func TestLifecyclePeer(t *testing.T) {
	scenario := os.Getenv("ACP_LIFECYCLE_PEER")
	if scenario == "" {
		return
	}
	output := json.NewEncoder(os.Stdout)
	write := func(row map[string]any) {
		row["jsonrpc"] = "2.0"
		if output.Encode(row) != nil {
			os.Exit(1)
		}
	}
	respond := func(id json.RawMessage, result any) { write(map[string]any{"id": id, "result": result}) }
	commands := func(session, name string) {
		write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{
			"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": name, "description": "fixture"}},
		}}})
	}
	config := func(current string) map[string]any {
		values := []map[string]string{{"name": "Default", "value": "default"}, {"name": "Opus", "value": "opus"}}
		if scenario == "unoffered" {
			values = values[:1]
		}
		return map[string]any{"id": "preferred-model", "name": "Model", "category": "model", "type": "select", "currentValue": current,
			"options": []map[string]any{{"group": "recommended", "name": "Recommended", "options": values}}}
	}
	permission := func(id int) {
		write(map[string]any{"id": id, "method": "session/request_permission", "params": map[string]any{
			"sessionId": "session", "toolCall": map[string]string{"title": "Approve fixture", "toolCallId": "tool"},
			"options": []map[string]string{{"name": "Allow", "kind": "allow_once", "optionId": "allow"}},
		}})
	}
	var promptID json.RawMessage
	cancelled, switched := false, false
	replies := map[string]bool{}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		var req struct {
			ID     json.RawMessage
			Method string
			Params map[string]any
			Result struct{ Outcome struct{ Outcome string } }
		}
		if json.Unmarshal(input.Bytes(), &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, map[string]int{"protocolVersion": 1})
		case "session/new":
			response := map[string]any{"sessionId": "session"}
			if scenario != "cancel" && scenario != "legacy" {
				current := "default"
				if scenario == "already-selected" {
					current = "opus"
				}
				response["configOptions"] = []any{map[string]string{"type": "future", "id": "extra"}, config(current)}
			}
			respond(req.ID, response)
			if scenario == "change-model" {
				commands("other-session", "crossed")
				commands("session", "after_new")
			}
		case "session/set_config_option":
			if scenario != "change-model" && scenario != "incorrect-ack" || req.Params["sessionId"] != "session" ||
				req.Params["configId"] != "preferred-model" || req.Params["value"] != "opus" {
				os.Exit(3)
			}
			if scenario == "incorrect-ack" {
				respond(req.ID, map[string]any{"configOptions": []any{config("default")}})
				continue
			}
			commands("session", "during_config")
			respond(req.ID, map[string]any{"configOptions": []any{config("opus")}})
			switched = true
		case "session/set_model":
			if scenario != "legacy" || req.Params["sessionId"] != "session" || req.Params["modelId"] != "opus" {
				os.Exit(4)
			}
			respond(req.ID, map[string]any{})
			switched = true
		case "session/prompt":
			if (scenario == "change-model" || scenario == "legacy") && !switched {
				os.Exit(5)
			}
			if scenario == "cancel" && !cancelled {
				promptID = req.ID
				permission(91)
				permission(92)
			} else {
				respond(req.ID, map[string]string{"stopReason": "end_turn"})
			}
		case "session/cancel":
			if scenario != "cancel" || req.Params["sessionId"] != "session" || len(promptID) == 0 || cancelled {
				os.Exit(6)
			}
			cancelled = true
			// This request races cancellation's release of the earlier two.
			permission(93)
		case "":
			id := string(req.ID)
			if !cancelled || (id != "91" && id != "92" && id != "93") || replies[id] || req.Result.Outcome.Outcome != "cancelled" {
				os.Exit(7)
			}
			replies[id] = true
			if len(replies) == 3 {
				respond(promptID, map[string]string{"stopReason": "cancelled"})
			}
		default:
			os.Exit(8)
		}
	}
	os.Exit(0)
}

func lifecycleClient(t *testing.T, scenario string, capture func(ACPUpdate)) (*agentacp.Sessions, context.Context) {
	t.Helper()
	runtime, err := New(Config{
		HarnessInstance: "lifecycle", WorkingDirectory: t.TempDir(),
		Command: os.Args[0], Args: []string{"-test.run=^TestLifecyclePeer$"}, Env: map[string]string{"ACP_LIFECYCLE_PEER": scenario},
		OnACPUpdate: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return agentacp.New(runtime), ctx
}

func TestInterruptCancelsPendingPermissionsBeforePromptCompletes(t *testing.T) {
	client, ctx := lifecycleClient(t, "cancel", nil)
	opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	ref := opened.GetSession().GetRef()
	if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "first", Text: "Wait for permissions"}); err != nil {
		t.Fatal(err)
	}
	var pending []string
	var result string
	for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
		if err != nil {
			t.Fatal(err)
		}
		if input := event.GetInput(); input != nil {
			pending = append(pending, input.RequestId)
			if len(pending) > 2 {
				t.Fatal("an approval opened after cancellation")
			}
			if len(pending) == 2 {
				request := &model.InterruptRequest{Session: ref, TurnId: "first", OperationId: "interrupt"}
				for range 2 {
					reply, err := client.Interrupt(ctx, request)
					if err != nil || reply.GetReceipt().GetStatus() != model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED {
						t.Fatalf("interrupt failed: %v %v", reply, err)
					}
				}
				for _, requestID := range pending {
					if _, err := client.Answer(ctx, &model.AnswerRequest{
						Session: ref, RequestId: requestID, OperationId: requestID,
						Answer: &model.AnswerRequest_OptionId{OptionId: "allow"},
					}); err == nil {
						t.Fatalf("stale answer accepted for %q", requestID)
					}
				}
			}
		}
		if end := event.GetTurnEnded(); end != nil && event.GetTurnId() == "first" {
			if end.Reason != "cancelled" {
				t.Fatalf("wrong terminal outcome: %v", end)
			}
			if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "second", Text: "Follow-up"}); err != nil {
				t.Fatal(err)
			}
		}
		if end := event.GetTurnEnded(); end != nil && event.GetTurnId() == "second" {
			result = end.Reason
			break
		}
	}
	if len(pending) != 2 || result != "end_turn" {
		t.Fatalf("two cancelled permissions should unblock the turn and a follow-up, got %d permissions and %q", len(pending), result)
	}
}

func TestSetupPreservesAnnouncementsAndConfirmsModel(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		wantErr string
	}{
		{"change-model", ""},
		{"already-selected", ""},
		{"legacy", ""},
		{"unoffered", "not offered"},
		{"incorrect-ack", "did not confirm"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			commands := make(chan ACPUpdate, 4)
			client, ctx := lifecycleClient(t, scenario.name, func(update ACPUpdate) { commands <- update })
			opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "lifecycle", Model: "opus"})
			if scenario.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), scenario.wantErr) || opened != nil {
					t.Fatalf("open with unverified model: %v %v", opened, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario.name == "change-model" {
				for _, name := range []string{"after_new", "during_config"} {
					select {
					case update := <-commands:
						if !strings.Contains(string(update.Body), name) || update.Session.GetSessionId() != "session" || update.TurnID != "" {
							t.Fatalf("unexpected setup update: %s", update.Body)
						}
					case <-ctx.Done():
						t.Fatalf("setup lost command %q", name)
					}
				}
				if len(commands) != 0 {
					t.Fatalf("unexpected extra setup update: %s", (<-commands).Body)
				}
			}
			ref := opened.GetSession().GetRef()
			if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "prompt", Text: "Use the approved model"}); err != nil {
				t.Fatal(err)
			}
			for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
				if err != nil {
					t.Fatal(err)
				}
				if end := event.GetTurnEnded(); end != nil {
					if end.Reason != "end_turn" {
						t.Fatalf("unexpected prompt outcome: %v", end)
					}
					return
				}
			}
			t.Fatal("missing prompt result")
		})
	}
}
