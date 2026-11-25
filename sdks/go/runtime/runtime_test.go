package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	"agent-acp.local/sdks/go/conformance"
	model "agent-acp.local/sdks/go/model"
)

// This subprocess speaks the real line-delimited, bidirectional wire protocol.
func TestAdapterProcess(t *testing.T) {
	if os.Getenv("TEST_AGENT_ACP_HELPER") != "yes" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	write := func(row map[string]any) {
		row["jsonrpc"] = "2.0"
		if err := enc.Encode(row); err != nil {
			os.Exit(2)
		}
	}
	respond := func(id json.RawMessage, v any) { write(map[string]any{"id": id, "result": v}) }
	sessionID := "native-" + strconv.Itoa(os.Getpid())
	var turn json.RawMessage
	update := func(kind string, row map[string]any) {
		row["sessionUpdate"] = kind
		params := map[string]any{"sessionId": sessionID, "update": row}
		if metadata := os.Getenv("TEST_AGENT_ACP_OUTER_PARAMS"); metadata != "" {
			params["_meta"] = map[string]any{"value": metadata, "nullable": nil}
			params["future"] = json.Number("9007199254740993")
		}
		write(map[string]any{"method": "session/update", "params": params})
	}
	permission := func(id int) {
		write(map[string]any{"id": id, "method": "session/request_permission", "params": map[string]any{"sessionId": sessionID, "toolCall": map[string]string{"title": "Allow reading file?", "toolCallId": "tool-7"}, "options": []map[string]string{{"optionId": "once", "name": "Allow once", "kind": "allow_once"}, {"optionId": "deny", "name": "Reject", "kind": "reject_once"}, {"optionId": "always", "kind": "allow_always", "name": "Always allow"}}}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	for scanner.Scan() {
		var row struct {
			ID             json.RawMessage
			Method         string
			Params, Result json.RawMessage
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			os.Exit(2)
		}
		switch row.Method {
		case "initialize":
			media := os.Getenv("TEST_AGENT_ACP_TEXT_ONLY") != "yes"
			respond(row.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "test"}, "agentCapabilities": map[string]any{
				"loadSession": os.Getenv("TEST_AGENT_ACP_NO_LOAD") != "yes", "sessionCapabilities": map[string]any{"list": map[string]any{}},
				"promptCapabilities": map[string]bool{"image": media, "audio": media},
			}})
		case "session/new":
			var req struct {
				Cwd        string
				MCPServers []struct {
					Name, Command string
					Args          []string
					Env           []map[string]string
				}
				Meta map[string]string `json:"_meta"`
			}
			json.Unmarshal(row.Params, &req)
			if req.Cwd == "" || len(req.MCPServers) != 1 || req.MCPServers[0].Args[0] != "literal `$HOME`" || req.Meta["test"] != "value" || req.MCPServers[0].Env[0]["value"] != "test" {
				os.Exit(3)
			}
			if os.Getenv("TEST_AGENT_ACP_CONFORMANCE") == "yes" {
				if err := os.WriteFile(filepath.Join(req.Cwd, sessionID+".fixture-session"), []byte(sessionID), 0600); err != nil {
					os.Exit(22)
				}
			}
			respond(row.ID, map[string]any{"sessionId": sessionID, "configOptions": []map[string]string{{"id": "model-select", "category": "model", "type": "select"}}})
		case "session/load":
			var req struct {
				SessionID  string
				Cwd        string
				MCPServers []json.RawMessage
			}
			json.Unmarshal(row.Params, &req)
			sessionID = req.SessionID
			if os.Getenv("TEST_AGENT_ACP_REPLAY") == "yes" {
				if req.Cwd != os.Getenv("TEST_AGENT_ACP_REPLAY_CWD") || req.MCPServers == nil || len(req.MCPServers) != 0 {
					os.Exit(12)
				}
				sessionID = "unrelated-peer"
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "wrong session"}})
				sessionID = req.SessionID
				update("user_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "literal prompt " + os.Getenv("TEST_AGENT_ACP_REPLAY_CHANGE")}})
				update("tool_call", map[string]any{"toolCallId": "tool-7", "title": "Read file", "status": "completed",
					"rawInput": map[string]string{"path": "sample.txt"}, "rawOutput": map[string]any{"result": map[string]any{
						"content": []map[string]string{{"type": "text", "text": "retained result"}},
					}}})
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "Assistant text"}})
			} else {
				update("user_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "old text"}})
			}
			permission(80)
			// Wait for a cancelled permission before completing the resume handshake.
			turn = row.ID
		case "session/prompt":
			if os.Getenv("TEST_AGENT_ACP_REPLAY") == "yes" {
				os.Exit(13)
			}
			var req struct {
				SessionID string
				Prompt    []map[string]string
			}
			json.Unmarshal(row.Params, &req)
			if sessionID != req.SessionID {
				os.Exit(4)
			}
			turn = row.ID
			text := req.Prompt[0]["text"]
			switch text {
			case "  Media before\n":
				if len(req.Prompt) != 5 || req.Prompt[1]["type"] != "image" || req.Prompt[1]["mimeType"] != "image/png" ||
					req.Prompt[1]["data"] != "AAH/" || req.Prompt[2]["text"] != "\tBetween\n" ||
					req.Prompt[3]["type"] != "audio" || req.Prompt[3]["mimeType"] != "audio/wav" ||
					req.Prompt[3]["data"] != "AgM=" || req.Prompt[4]["text"] != "After  " {
					os.Exit(14)
				}
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "media prompt verified"}})
				respond(turn, map[string]string{"stopReason": "end_turn"})
			case "wait":
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "waiting"}})
			case "disconnect":
				os.Exit(0)
			case "Fingerprint the attached conformance image.", "Fingerprint the attached conformance audio.":
				kind, mime := "image", "image/png"
				if strings.HasSuffix(text, "audio.") {
					kind, mime = "audio", "audio/wav"
				}
				if len(req.Prompt) != 2 || req.Prompt[1]["type"] != kind || req.Prompt[1]["mimeType"] != mime {
					os.Exit(21)
				}
				data, err := base64.StdEncoding.DecodeString(req.Prompt[1]["data"])
				if err != nil {
					os.Exit(21)
				}
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": fmt.Sprintf("sha256:%x", sha256.Sum256(data))}})
				respond(turn, map[string]string{"stopReason": "end_turn"})
			case conformancePrompt:
				update("tool_call", map[string]any{"toolCallId": "tool-7", "title": "Read file", "status": "in_progress"})
				permission(71)
			case "ask":
				update("tool_call", map[string]any{"toolCallId": "tool-7", "title": "Read file", "status": "in_progress"})
				permission(70)
			default:
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": text}})
				respond(turn, map[string]string{"stopReason": "end_turn"})
			}
		case "session/cancel":
			if turn == nil {
				os.Exit(6)
			}
			respond(turn, map[string]string{"stopReason": "cancelled"})
			turn = nil
		case "session/set_config_option":
			var req struct{ ConfigID, Value string }
			json.Unmarshal(row.Params, &req)
			if req.ConfigID != "model-select" || req.Value != "model-one" {
				os.Exit(9)
			}

			respond(row.ID, map[string]any{})
		case "session/list":
			if table := os.Getenv("TEST_AGENT_ACP_LIST_PAGES"); table != "" {
				var pages map[string]json.RawMessage
				var req struct{ Cursor string }
				raw, err := os.ReadFile(table)
				if err != nil || json.Unmarshal(raw, &pages) != nil || json.Unmarshal(row.Params, &req) != nil || pages[req.Cursor] == nil {
					os.Exit(24)
				}
				respond(row.ID, pages[req.Cursor])
				continue
			}
			if os.Getenv("TEST_AGENT_ACP_CONFORMANCE") == "yes" {
				var req struct{ Cwd string }
				if json.Unmarshal(row.Params, &req) != nil || req.Cwd == "" {
					os.Exit(23)
				}
				files, err := filepath.Glob(filepath.Join(req.Cwd, "*.fixture-session"))
				if err != nil {
					os.Exit(23)
				}
				var sessions []map[string]string
				for _, file := range files {
					sessions = append(sessions, map[string]string{"sessionId": strings.TrimSuffix(filepath.Base(file), ".fixture-session"), "cwd": req.Cwd})
				}
				respond(row.ID, map[string]any{"sessions": sessions})
				continue
			}
			respond(row.ID, map[string]any{"sessions": []map[string]string{{"sessionId": "a", "title": "A"}, {"sessionId": "b", "title": "B"}}})
		case "":
			var result struct {
				Outcome struct{ Outcome, OptionID string }
			}
			json.Unmarshal(row.Result, &result)
			if string(row.ID) == "80" {
				if result.Outcome.Outcome != "cancelled" {
					os.Exit(8)
				}
				respond(turn, map[string]any{})
				turn = nil
				continue
			}
			if (string(row.ID) != "70" && string(row.ID) != "71") || result.Outcome.Outcome != "selected" || result.Outcome.OptionID != "once" {
				os.Exit(7)
			}
			if string(row.ID) == "71" {
				update("tool_call_update", map[string]any{"toolCallId": "tool-7", "status": "completed", "content": []map[string]any{
					{"type": "content", "content": map[string]string{"type": "text", "text": "fixture bytes"}},
				}})
				update("fixture_resource", map[string]any{"uri": "fixture://missing", "reason": "resource unavailable"})
			}
			update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "permission received"}})
			respond(turn, map[string]string{"stopReason": "end_turn"})
		default:
			write(map[string]any{"id": row.ID, "error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
	os.Exit(0)
}

func TestMediaPromptUsesNegotiatedACPContentBlocks(t *testing.T) {
	media := []*model.PromptPart{
		{Content: &model.PromptPart_Text{Text: "  Media before\n"}},
		{Content: &model.PromptPart_Image{Image: &model.PromptMedia{MimeType: "image/png", Data: []byte{0, 1, 255}}}},
		{Content: &model.PromptPart_Text{Text: "\tBetween\n"}},
		{Content: &model.PromptPart_Audio{Audio: &model.PromptMedia{MimeType: "audio/wav", Data: []byte{2, 3}}}},
		{Content: &model.PromptPart_Text{Text: "After  "}},
	}
	t.Run("advertised", func(t *testing.T) {
		_, client, ctx := newTestRuntime(t)
		session, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "unit"})
		if err != nil || !session.GetSession().GetCapabilities().GetCanPromptImage() || !session.GetSession().GetCapabilities().GetCanPromptAudio() {
			t.Fatalf("prompt capabilities did not survive negotiation: %v, %v", session, err)
		}
		ref := session.GetSession().GetRef()
		if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "ambiguous", Text: "overlap", Parts: media}); err == nil {
			t.Fatal("accepted a mixed legacy and ordered prompt")
		}
		invalid := []*model.PromptPart{{Content: &model.PromptPart_Image{Image: &model.PromptMedia{MimeType: "audio/wav", Data: []byte{1}}}}}
		if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "invalid", Parts: invalid}); err == nil {
			t.Fatal("accepted an image with an audio MIME type")
		}
		req := &model.SendRequest{Session: ref, OperationId: "media", Parts: media}
		sent, err := client.Send(ctx, req)
		if err != nil || sent.GetReceipt().GetStatus() != model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED {
			t.Fatalf("media prompt was not accepted: %v, %v", sent, err)
		}
		if _, err := client.Send(ctx, req); err != nil {
			t.Fatalf("identical media prompt did not recover its receipt: %v", err)
		}
		observed := false
		for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
			if err != nil {
				t.Fatal(err)
			}
			if text := event.GetText(); text.GetSpeaker() == model.Speaker_SPEAKER_USER &&
				(!strings.Contains(text.GetText(), "[ACP image] image/png") || strings.Contains(text.GetText(), "AAH/")) {
				t.Fatalf("incorrect prompt preview: %q", text.GetText())
			}
			if event.GetText().GetText() == "media prompt verified" {
				observed = true
			}
			if event.GetTurnEnded() != nil {
				break
			}
		}
		if !observed {
			t.Fatal("adapter did not confirm the ordered prompt")
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		t.Setenv("TEST_AGENT_ACP_TEXT_ONLY", "yes")
		_, client, ctx := newTestRuntime(t)
		ref := opened(t, client, ctx)
		if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "unsupported", Parts: media}); err == nil ||
			!strings.Contains(err.Error(), "does not support image prompts") {
			t.Fatalf("unsupported prompt did not fail before dispatch: %v", err)
		}
		if _, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: "plain", Text: "plain still works"}); err != nil {
			t.Fatal(err)
		}
	})
}

func newTestRuntime(t *testing.T) (*Runtime, *agentacp.Sessions, context.Context) {
	t.Helper()
	cmd, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{OncePermissionsOnly: true, HarnessInstance: "unit", Command: cmd, Args: []string{"-test.run=^TestAdapterProcess$"}, WorkingDirectory: t.TempDir(), Env: map[string]string{"TEST_AGENT_ACP_HELPER": "yes"}, SessionMeta: map[string]any{"test": "value"}, MCPServers: []MCPServer{{Name: "commander", Command: "commander", Args: []string{"literal `$HOME`"}, Env: map[string]string{"key": "test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	return r, agentacp.New(r), ctx
}
func opened(t *testing.T, c *agentacp.Sessions, ctx context.Context) *model.SessionRef {
	t.Helper()
	resp, err := c.Open(ctx, &model.OpenRequest{HarnessInstance: "unit", Model: "model-one"})
	if err != nil {
		t.Fatal(err)
	}
	caps := resp.Session.Capabilities
	if !caps.CanAnswer || !caps.CanInterrupt || !caps.CanResume || !caps.CanListSessions || caps.CanReadHistory {
		t.Fatalf("capabilities %v", caps)
	}
	return resp.Session.Ref
}
func recv(t *testing.T, stream agentacp.Stream) *model.Observation {
	t.Helper()
	evt := new(model.Observation)
	if err := stream.Recv(evt); err != nil {
		t.Fatal(err)
	}
	return evt
}
func send(t *testing.T, c *agentacp.Sessions, ctx context.Context, ref *model.SessionRef, id, text string) {
	t.Helper()
	rsp, err := c.Send(ctx, &model.SendRequest{Session: ref, OperationId: id, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.Receipt.Status != model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED {
		t.Fatal(rsp.Receipt)
	}
}
func observe(t *testing.T, r *Runtime, ctx context.Context, ref *model.SessionRef, after string) agentacp.Stream {
	t.Helper()
	stream, err := r.Stream(ctx, "Observe", &model.ObserveRequest{Session: ref, AfterCursor: after})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stream.Close)
	return stream
}
func TestPermissionIsScopedAndObserverCanReattach(t *testing.T) {
	r, c, ctx := newTestRuntime(t)
	a := opened(t, c, ctx)
	b := opened(t, c, ctx)
	send(t, c, ctx, a, "op-1", "ask")
	s := observe(t, r, ctx, a, "")
	if got := recv(t, s); got.GetText().Text != "ask" || got.TurnId != "op-1" {
		t.Fatal(got)
	}
	if got := recv(t, s); got.GetTool().CallId != "tool-7" {
		t.Fatal(got)
	}
	input := recv(t, s)
	if len(input.GetInput().Choices) != 2 {
		t.Fatal("persistent choices must be excluded", input)
	}
	s.Close()
	answer := &model.AnswerRequest{Session: b, OperationId: "reply", RequestId: input.GetInput().RequestId, Answer: &model.AnswerRequest_OptionId{OptionId: "once"}}
	if _, err := c.Answer(ctx, answer); err == nil {
		t.Fatal("cross-session answer accepted")
	}
	answer.Session = a
	answer.Answer = &model.AnswerRequest_OptionId{OptionId: "invalid"}
	if _, err := c.Answer(ctx, answer); err == nil {
		t.Fatal("unknown option accepted")
	}
	answer.Answer = &model.AnswerRequest_OptionId{OptionId: "once"}
	if _, err := c.Answer(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Answer(ctx, answer); err != nil {
		t.Fatal("duplicate answer", err)
	}
	answer.OperationId = "second-answer"
	if _, err := c.Answer(ctx, answer); err == nil {
		t.Fatal("stale request accepted")
	}
	s = observe(t, r, ctx, a, input.Cursor)
	if reply := recv(t, s); reply.GetText().Text != "permission received" {
		t.Fatal(reply)
	}
	if end := recv(t, s); end.GetTurnEnded().Reason != "end_turn" {
		t.Fatal(end)
	}
	if _, err := c.Send(ctx, &model.SendRequest{Session: a, OperationId: "op-1", Text: "different"}); err == nil {
		t.Fatal("conflicting operation accepted")
	}
}
func TestImmediateInterruptAndLiteralMessage(t *testing.T) {
	r, c, ctx := newTestRuntime(t)
	ref := opened(t, c, ctx)
	send(t, c, ctx, ref, "turn-1", "wait")
	if _, err := c.Interrupt(ctx, &model.InterruptRequest{Session: ref, OperationId: "bad", TurnId: "other"}); err == nil {
		t.Fatal("wrong turn accepted")
	}
	if _, err := c.Interrupt(ctx, &model.InterruptRequest{Session: ref, OperationId: "stop", TurnId: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	stream := observe(t, r, ctx, ref, "")
	for {
		o := recv(t, stream)
		if end := o.GetTurnEnded(); end != nil {
			if end.Reason != "cancelled" {
				t.Fatal(end)
			}
			break
		}
	}
	text := "  dollar `$HOME` and \"a b\"\n\tthen stop\n"
	send(t, c, ctx, ref, "turn-2", text)
	if user := recv(t, stream); user.GetText().Text != text {
		t.Fatal(user)
	}
	if reply := recv(t, stream); reply.GetText().Text != text || reply.GetText().Speaker != model.Speaker_SPEAKER_ASSISTANT {
		t.Fatal(reply)
	}
	if end := recv(t, stream); end.GetTurnEnded().Reason != "end_turn" {
		t.Fatal(end)
	}
}
func TestDisconnectedOperationSurvivesResume(t *testing.T) {
	r, c, ctx := newTestRuntime(t)
	ref := opened(t, c, ctx)
	send(t, c, ctx, ref, "lost", "disconnect")
	stream := observe(t, r, ctx, ref, "")
	recv(t, stream)
	if recv(t, stream).GetTurnEnded() == nil {
		t.Fatal("missing interrupted turn")
	}
	if err := stream.Recv(new(model.Observation)); err != io.EOF {
		t.Fatalf("expected closed stream, got %v", err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Resume(ctx, &model.ResumeRequest{Session: ref}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	retry, err := c.Send(ctx, &model.SendRequest{Session: ref, OperationId: "lost", Text: "disconnect"})
	if err != nil || retry.Receipt.Status != model.DeliveryStatus_DELIVERY_STATUS_UNKNOWN {
		t.Fatal(retry, err)
	}
	send(t, c, ctx, ref, "next", "hello")
	fresh := observe(t, r, ctx, ref, "")
	if got := recv(t, fresh); got.GetText().Text != "hello" {
		t.Fatal("resume replay leaked", got)
	}
}

func TestNativeHistoryNeedsNoExecutable(t *testing.T) {
	cfg := Config{HarnessInstance: "history", ReadHistory: func(_ context.Context, q *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
		if q.Session.SessionId != "old" {
			return nil, fmt.Errorf("wrong session")
		}
		return &model.ReadHistoryResponse{Observations: []*model.Observation{{Session: q.Session, Event: &model.Observation_Text{Text: &model.TextUpdate{Text: "retained"}}}}}, nil
	}}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := agentacp.New(r)
	ref := &model.SessionRef{HarnessInstance: "history", SessionId: "old"}
	got, err := c.ReadHistory(context.Background(), &model.ReadHistoryRequest{Session: ref})
	if err != nil || got.Observations[0].GetText().Text != "retained" {
		t.Fatal(got, err)
	}
	if _, err := c.Answer(context.Background(), &model.AnswerRequest{Session: ref, OperationId: "no"}); err == nil {
		t.Fatal("history created live session")
	}
}
func TestNativeSessionListingPages(t *testing.T) {
	_, c, ctx := newTestRuntime(t)
	q := &model.ListSessionsRequest{HarnessInstance: "unit", Limit: 1}
	first, err := c.ListSessions(ctx, q)
	if err != nil || len(first.Sessions) != 1 || first.Sessions[0].GetRef().SessionId != "a" || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	q.Cursor = first.NextCursor
	second, err := c.ListSessions(ctx, q)
	if err != nil || len(second.Sessions) != 1 || second.Sessions[0].GetRef().SessionId != "b" || second.NextCursor != "" {
		t.Fatal(second, err)
	}
}

// Some installed ACP agents advertise session/list but return their global
// directory even when asked for one checkout. The SDK must apply the scope on
// every raw page and on every continuation through a partially consumed page.
func TestSessionListingEnforcesDirectoryAcrossNativeAndPortablePages(t *testing.T) {
	rt, client, ctx := newTestRuntime(t)
	selected, other := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "selected-link")
	if err := os.Symlink(selected, link); err != nil {
		t.Fatal(err)
	}
	row := func(id, cwd string) map[string]string {
		return map[string]string{"sessionId": id, "cwd": cwd, "title": "title-" + id}
	}
	pages := map[string]any{
		"": map[string]any{"sessions": []map[string]string{row("foreign", other), row("relative", "relative")}, "nextCursor": "native-2"},
		"native-2": map[string]any{"sessions": []any{row("a", selected), map[string]string{"sessionId": "no-cwd"},
			map[string]any{"sessionId": "additional", "cwd": other, "additionalDirectories": []string{selected}}, row("b", link), row("after", other)}, "nextCursor": "native-3"},
		"native-3": map[string]any{"sessions": []map[string]string{row("c", selected)}},
	}
	source := filepath.Join(t.TempDir(), "pages.json")
	raw, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	rt.config.Env["TEST_AGENT_ACP_LIST_PAGES"] = source
	q := &model.ListSessionsRequest{HarnessInstance: "unit", WorkingDirectory: selected, Limit: 1}
	var got []string
	for i := 0; ; i++ {
		if i > 4 {
			t.Fatal("listing failed to advance")
		}
		result, err := client.ListSessions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sessions) > 1 {
			t.Fatal("listing exceeded its requested page size")
		}
		for _, session := range result.Sessions {
			got = append(got, session.GetRef().GetSessionId())
		}
		if i == 0 {
			if len(result.Sessions) != 0 || result.NextCursor == "" {
				t.Fatal("filtered-empty native page did not preserve the continuation")
			}
			otherQuery := &model.ListSessionsRequest{HarnessInstance: "unit", WorkingDirectory: other, Cursor: result.NextCursor}
			if _, err := client.ListSessions(ctx, otherQuery); err == nil {
				t.Fatal("new cursor accepted a different directory")
			}
		}
		if result.NextCursor == "" {
			break
		}
		q.Cursor = result.NextCursor
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("scoped session IDs: %v", got)
	}
	// Legacy offsets counted rows on the original native page. Apply that
	// position before filtering, including when it points at an excluded row.
	token, _ := json.Marshal(pageCursor{Page: "native-2", Offset: 1})
	q.Cursor = base64.RawURLEncoding.EncodeToString(token)
	gotPage, err := client.ListSessions(ctx, q)
	if err != nil || len(gotPage.GetSessions()) != 1 || gotPage.Sessions[0].GetRef().GetSessionId() != "b" {
		t.Fatal("old cursor resumed at the wrong record", err)
	}
	q.WorkingDirectory, q.Cursor, q.Limit = "", "", 0
	all, err := client.ListSessions(ctx, q)
	if err != nil || len(all.GetSessions()) != 2 || all.Sessions[0].GetRef().GetSessionId() != "foreign" {
		t.Fatal("unfiltered listing lost native records", err)
	}
}

func TestSessionListingAbsentWorkspaceRetainsOnlyItsExactPath(t *testing.T) {
	rt, client, ctx := newTestRuntime(t)
	cwd := filepath.Join(t.TempDir(), "removed")
	pages := map[string]any{"": map[string]any{"sessions": []map[string]string{
		{"sessionId": "archive", "cwd": cwd}, {"sessionId": "lookalike", "cwd": cwd + "-other"}, {"sessionId": "unknown"},
	}}}
	raw, _ := json.Marshal(pages)
	file := filepath.Join(t.TempDir(), "pages.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	rt.config.Env["TEST_AGENT_ACP_LIST_PAGES"] = file
	result, err := client.ListSessions(ctx, &model.ListSessionsRequest{HarnessInstance: "unit", WorkingDirectory: cwd})
	if err != nil || len(result.GetSessions()) != 1 || result.Sessions[0].Ref.SessionId != "archive" {
		t.Fatal("archived directory listing lost its scope", err)
	}
}

func TestObserverReportsGapWhenLiveWindowExpires(t *testing.T) {
	s := newSession()
	s.info = &model.Session{Ref: &model.SessionRef{HarnessInstance: "test", SessionId: "s"}}
	stream := &observations{ctx: context.Background(), session: s}
	for i := 0; i <= liveWindow; i++ {
		s.emit(&model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Text: strconv.Itoa(i)}}})
	}
	first := recv(t, stream)
	if first.GetGap() == nil || first.Cursor != s.cursor(1) {
		t.Fatal(first)
	}
	if next := recv(t, stream); next.GetText().Text != "1" {
		t.Fatal(next)
	}
}

func TestSourceUpdateCarriesOriginalBodyAndObservationIdentity(t *testing.T) {
	r, c, ctx := newTestRuntime(t)
	captured := make(chan ACPUpdate, 4)
	r.config.OnACPUpdate = func(update ACPUpdate) {
		captured <- update
	}
	ref := opened(t, c, ctx)
	send(t, c, ctx, ref, "source-turn", "hello from source")
	stream := observe(t, r, ctx, ref, "")
	defer stream.Close()
	event := recv(t, stream)
	for event.GetText().GetSpeaker() == model.Speaker_SPEAKER_USER {
		event = recv(t, stream)
	}
	select {
	case source := <-captured:
		if source.Cursor != event.Cursor || source.TurnID != "source-turn" ||
			source.Session.GetHarnessInstance() != ref.HarnessInstance || source.Session.GetSessionId() != ref.SessionId ||
			!strings.Contains(string(source.Body), "hello from source") || !strings.Contains(string(source.Body), "agent_message_chunk") {
			t.Fatalf("lost source or observation binding: %+v", source)
		}
	case <-ctx.Done():
		t.Fatal("source update did not arrive")
	}
}

func TestMCPRawResultProvidesTypedToolPreview(t *testing.T) {
	raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"ask-1","status":"completed","rawOutput":{"result":{"content":[{"type":"text","text":"Request q-fixture"},{"type":"image","mimeType":"image/png","data":"INLINE_SECRET"}]},"error":null,"other":"PRIVATE_PEER_FIELD"}}`)
	tool := normalizeUpdate(raw).GetTool()
	if tool.GetCallId() != "ask-1" || tool.GetPhase() != "completed" ||
		tool.GetSummary() != "Request q-fixture\n[ACP image] image/png" {
		t.Fatal(tool)
	}
}

func TestMCPFailuresPreservePartialOutputAndSource(t *testing.T) {
	for _, test := range []struct {
		name, fields, phase, summary string
	}{
		{"direct result", `"status":"completed","rawOutput":{"isError":true,"structuredContent":{"details":"original private detail"},"content":[{"type":"text","text":"Partial result"},{"type":"audio","mimeType":"audio/wav","data":"PRIVATE_AUDIO"}]}`,
			"failed", "[Structured MCP result]\nPartial result\n[ACP audio] audio/wav"},
		{"wrapped result", `"status":"completed","content":[{"type":"content","content":{"type":"text","text":"ACP partial result"}}],"rawOutput":{"result":{"isError":true,"content":[{"type":"text","text":"ACP partial result"}]}}`,
			"failed", "ACP partial result"},
		{"protocol failure", `"status":"completed","content":[{"type":"content","content":{"type":"text","text":"ACP partial result"}}],"rawOutput":{"error":{"code":-32603,"message":"Tool service disconnected","data":"PRIVATE_ERROR"}}`,
			"failed", "Tool service disconnected\nACP partial result"},
		{"cancelled ACP call", `"status":"cancelled","rawOutput":{"isError":true,"content":[{"type":"text","text":"Stopped"}]}`,
			"cancelled", "Stopped"},
		{"direct success", `"status":"completed","rawOutput":{"isError":false,"content":[{"type":"text","text":"MCP output"}]}`,
			"completed", "MCP output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"fixture",` + test.fields + "}")
			tool := normalizeUpdate(raw).GetTool()
			if tool.GetPhase() != test.phase || tool.GetSummary() != test.summary {
				t.Fatalf("incorrect tool preview: %v", tool)
			}
			history := replayRecords(raw)
			if len(history) != 2 || history[0].GetTool().GetPhase() != test.phase ||
				history[0].GetTool().GetSummary() != test.summary || history[1].GetExtension().GetType() != ACPReplaySource {
				t.Fatalf("replay diverged from the live preview or dropped the original record: %v", history)
			}
			original := history[1].GetExtension().GetData()
			for _, marker := range []string{"original private detail", "PRIVATE_AUDIO", "PRIVATE_ERROR"} {
				if bytes.Contains(raw, []byte(marker)) && !bytes.Contains(original, []byte(marker)) {
					t.Fatalf("replay source omitted %q", marker)
				}
			}
		})
	}
}

func TestToolSummaryUsesDisplayContent(t *testing.T) {
	raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","kind":"other","status":"completed","rawOutput":{"secret":"private peer metadata"},"content":[{"type":"content","content":{"type":"text","text":"Worker created."}},{"type":"diff","path":"README.md","newText":"not a preview"},{"type":"terminal","terminalId":"term-2"}]}`)
	tool := normalizeUpdate(raw).GetTool()
	if tool.CallId != "call-1" || tool.Summary != "Worker created.\nDiff README.md\nTerminal term-2" {
		t.Fatal(tool)
	}
	long := strings.Repeat("界", 1030)
	raw, err := json.Marshal([]map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": long}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := toolSummary(raw, nil, "other"); got != strings.Repeat("界", 1024)+"…" {
		t.Fatal("invalid truncation length", len(got))
	}
}

func TestToolSummaryIncludesVisibleResourceDescriptors(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"content","content":{"type":"text","text":"partial result"}},
		{"type":"content","content":{"type":"resource_link","name":"plan.md","uri":"fixture://plan","mimeType":"text/markdown","size":0}},
		{"type":"content","content":{"type":"resource","resource":{"uri":"fixture://inline","text":"inline draft"}}},
		{"type":"content","content":{"type":"image","mimeType":"image/png","data":"BINARY_MARKER"}},
		{"type":"content","content":{"type":"future_block","extra":"unknown"}}
	]`)
	got, _ := toolSummary(raw, nil, "other")
	for _, part := range []string{"partial result", "plan.md fixture://plan text/markdown 0 bytes", "fixture://inline", "inline draft", "[ACP image] image/png", "[ACP content: future_block]"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing preview %q in %q", part, got)
		}
	}
	if strings.Contains(got, "BINARY_MARKER") {
		t.Fatalf("binary data leaked into the preview: %q", got)
	}
}

const conformancePrompt = "  Read the fixture. Preserve $HOME and \"quotes\".\n"

func TestExecutableAdapterPortableConformance(t *testing.T) {
	rt, _, ctx := newTestRuntime(t)
	rt.config.Env["TEST_AGENT_ACP_CONFORMANCE"] = "yes"
	err := conformance.Check(ctx, rt, conformance.Target{HarnessInstance: rt.config.HarnessInstance, WorkingDirectory: rt.config.WorkingDirectory},
		conformance.Scenario{
			Prompt: conformancePrompt, PermissionPrompt: "Allow reading file?", ChoiceLabel: "Allow once",
			ToolResultMarker: "fixture bytes", CompletionMarker: "permission received",
			InterruptPrompt: "wait", InterruptReady: "waiting",
			ExtensionType: "fixture_resource", ExtensionDataMarker: "resource unavailable",
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadACPReplayPages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd := t.TempDir()
	ref := &model.SessionRef{HarnessInstance: "replay", SessionId: "saved-session"}
	start := func(extra map[string]string) *agentacp.Sessions {
		t.Helper()
		env := map[string]string{"TEST_AGENT_ACP_HELPER": "yes", "TEST_AGENT_ACP_REPLAY": "yes", "TEST_AGENT_ACP_REPLAY_CWD": cwd}
		for key, value := range extra {
			env[key] = value
		}
		rt, err := New(Config{HarnessInstance: ref.HarnessInstance, Command: os.Args[0], Args: []string{"-test.run=^TestAdapterProcess$"},
			WorkingDirectory: cwd, Env: env, ReplayHistory: true, MCPServers: []MCPServer{{Name: "must-not-launch", Command: "forbidden"}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rt.Close() })
		return agentacp.New(rt)
	}

	sdk := start(nil)
	first, err := sdk.ReadHistory(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 2})
	if err != nil || len(first.Observations) != 2 || first.NextCursor == "" ||
		first.Observations[0].GetText().GetText() != "literal prompt " || first.Observations[1].GetExtension().GetType() != ACPReplaySource {
		t.Fatalf("first ACP replay page: %v, %v", first, err)
	}
	savedCursor := first.NextCursor
	records := append([]*model.Observation(nil), first.Observations...)
	for cursor := savedCursor; cursor != ""; {
		page, err := sdk.ReadHistory(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 2, Cursor: cursor})
		if err != nil || page.NextCursor == cursor || len(records) >= 7 {
			t.Fatalf("replay page failed to advance: %v, %v", page, err)
		}
		records = append(records, page.Observations...)
		cursor = page.NextCursor
	}
	if len(records) != 7 || records[2].GetTool().GetPhase() != "tool_call" ||
		records[3].GetTool().GetSummary() != "retained result" ||
		!strings.Contains(string(records[4].GetExtension().GetData()), "sample.txt") ||
		records[5].GetText().GetText() != "Assistant text" {
		t.Fatalf("ACP replay lost ordered call, result or source: %v", records)
	}
	for _, record := range records {
		if record.GetSession().GetSessionId() != ref.SessionId || record.Cursor == "" || record.GetInput() != nil {
			t.Fatalf("ACP replay returned an actionable or unrelated record: %v", record)
		}
	}
	if _, err := sdk.ReadHistory(ctx, &model.ReadHistoryRequest{Session: &model.SessionRef{HarnessInstance: ref.HarnessInstance, SessionId: "other"}, Cursor: savedCursor}); err == nil {
		t.Fatal("replay cursor crossed sessions")
	}
	if _, err := start(map[string]string{"TEST_AGENT_ACP_REPLAY_CHANGE": "edited"}).ReadHistory(ctx,
		&model.ReadHistoryRequest{Session: ref, Cursor: savedCursor}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("modified replay prefix did not invalidate cursor: %v", err)
	}
	if _, err := start(map[string]string{"TEST_AGENT_ACP_NO_LOAD": "yes"}).ReadHistory(ctx,
		&model.ReadHistoryRequest{Session: ref}); err == nil || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("unadvertised load was not rejected: %v", err)
	}
}
