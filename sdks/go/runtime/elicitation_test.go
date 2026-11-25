package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
)

func TestElicitationUsesSessionScopeAndDistinctAnswers(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			requested := make(chan ElicitationRequest, 3)
			completed := make(chan ElicitationRequest, 1)
			cwd := t.TempDir()
			r, err := New(Config{
				HarnessInstance: "elicitation", Command: os.Args[0], Args: []string{"-test.run=^TestElicitationPeer$"},
				WorkingDirectory: cwd, ProtocolVersion: version,
				Env: map[string]string{"ACP_ELICITATION_PEER": "1"},
				Elicitation: &Elicitation{
					Form: true, URL: true,
					Create: func(ctx context.Context, request ElicitationRequest) (ElicitationResponse, error) {
						requested <- request
						switch request.Message {
						case "Block":
							if err := os.WriteFile(filepath.Join(cwd, "elicitation-entered"), nil, 0600); err != nil {
								return ElicitationResponse{}, err
							}
							<-ctx.Done()
							return ElicitationResponse{}, ctx.Err()
						case "Label":
							return ElicitationResponse{Action: "accept", Content: json.RawMessage(`{"value":"literal $HOME"}`)}, nil
						}
						return ElicitationResponse{Action: "accept"}, nil
					},
					Complete: func(_ context.Context, request ElicitationRequest) {
						completed <- request
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := agentacp.New(r)
			opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "elicitation"})
			if err != nil {
				t.Fatal(err)
			}
			ref := opened.Session.Ref
			stream := observe(t, r, ctx, ref, "")
			send(t, client, ctx, ref, "turn", "fixture input")
			for seen := 0; ; seen++ {
				if seen > 10 {
					t.Fatal("elicitation prevented the original turn from completing")
				}
				if terminal := recv(t, stream).GetTurnEnded(); terminal != nil {
					if terminal.Reason != "end_turn" {
						t.Fatal(terminal)
					}
					break
				}
			}
			for _, want := range []string{"Label", "Confirm URL", "Block"} {
				select {
				case req := <-requested:
					if req.SessionID != ref.SessionId || req.Message != want {
						t.Fatalf("wrong request was presented to user: %+v", req)
					}
				default:
					t.Fatal("addressed request was not presented")
				}
			}
			select {
			case notice := <-completed:
				if notice.ElicitationID != "external-1" || notice.URL != "https://example.test/confirm?x=%24" {
					t.Fatalf("external completion lost its originating request: %+v", notice)
				}
			default:
				t.Fatal("matched external completion was not delivered")
			}
		})
	}
}

func TestElicitationPeer(t *testing.T) {
	if os.Getenv("ACP_ELICITATION_PEER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	send := func(fields map[string]any) {
		fields["jsonrpc"] = "2.0"
		if encoder.Encode(fields) != nil {
			os.Exit(2)
		}
	}
	recv := func() (frame struct {
		ID     json.RawMessage
		Method string
		Params json.RawMessage
		Result json.RawMessage
		Error  *struct{ Code int }
	}) {
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(3)
		}
		return
	}
	initialize := recv()
	var init struct{ ProtocolVersion int }
	_ = json.Unmarshal(initialize.Params, &init)
	for _, field := range [][]byte{[]byte(`"elicitation"`), []byte(`"form":{}`), []byte(`"url":{}`)} {
		if !bytes.Contains(initialize.Params, field) {
			os.Exit(4)
		}
	}
	send(map[string]any{"id": initialize.ID, "result": map[string]any{
		"protocolVersion": init.ProtocolVersion, "capabilities": map[string]any{"session": struct{}{}},
	}})
	create := recv()
	send(map[string]any{"id": create.ID, "result": map[string]string{"sessionId": "active"}})
	prompt := recv()
	if init.ProtocolVersion == 2 {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "active", "update": map[string]any{
			"sessionUpdate": "state_update", "state": "running", "background": []any{},
		}}})
	}
	request := func(id int, params ElicitationRequest) {
		send(map[string]any{"id": id, "method": "elicitation/create", "params": params})
	}
	form := ElicitationRequest{SessionID: "foreign", Mode: "form", Message: "Label", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)}
	request(1, form)
	if got := recv(); got.Error == nil || got.Error.Code != -32602 {
		os.Exit(5)
	}
	form.SessionID = "active"
	request(2, form)
	if got := recv(); string(got.Result) != `{"action":"accept","content":{"value":"literal $HOME"}}` {
		os.Exit(6)
	}
	url := ElicitationRequest{SessionID: "active", Mode: "url", Message: "Confirm URL", ElicitationID: "external-1", URL: "https://example.test/confirm?x=%24"}
	request(3, url)
	if got := recv(); string(got.Result) != `{"action":"accept"}` {
		os.Exit(7)
	}
	for _, id := range []string{"unknown", "external-1", "external-1"} {
		send(map[string]any{"method": "elicitation/complete", "params": map[string]string{"elicitationId": id}})
	}
	form.Message = "Block"
	request(4, form)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat("elicitation-entered"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(9)
		}
		time.Sleep(time.Millisecond)
	}
	send(map[string]any{"method": "$/cancel_request", "params": map[string]int{"requestId": 4}})
	if got := recv(); string(got.Result) != `{"action":"cancel"}` {
		os.Exit(8)
	}
	if init.ProtocolVersion == 2 {
		send(map[string]any{"id": prompt.ID, "result": map[string]string{"messageId": "inserted"}})
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "active", "update": map[string]any{
			"sessionUpdate": "state_update", "state": "idle", "stopReason": "end_turn", "background": []any{},
		}}})
	} else {
		send(map[string]any{"id": prompt.ID, "result": map[string]string{"stopReason": "end_turn"}})
	}
	for scanner.Scan() {
	}
	os.Exit(0)
}
