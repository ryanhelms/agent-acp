package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
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
		write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": row}})
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
			respond(row.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "test"}, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"list": map[string]any{}}}})
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
			respond(row.ID, map[string]any{"sessionId": sessionID, "configOptions": []map[string]string{{"id": "model-select", "category": "model", "type": "select"}}})
		case "session/load":
			var req struct{ SessionID string }
			json.Unmarshal(row.Params, &req)
			sessionID = req.SessionID
			update("user_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "old text"}})
			permission(80)
			// Wait for a cancelled permission before completing the resume handshake.
			turn = row.ID
		case "session/prompt":
			var req struct {
				SessionID string
				Prompt    []struct{ Text string }
			}
			json.Unmarshal(row.Params, &req)
			if sessionID != req.SessionID {
				os.Exit(4)
			}
			turn = row.ID
			text := req.Prompt[0].Text
			switch text {
			case "wait":
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "waiting"}})
			case "disconnect":
				os.Exit(0)
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
			if string(row.ID) != "70" || result.Outcome.Outcome != "selected" || result.Outcome.OptionID != "once" {
				os.Exit(7)
			}
			update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "permission received"}})
			respond(turn, map[string]string{"stopReason": "end_turn"})
		default:
			write(map[string]any{"id": row.ID, "error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
	os.Exit(0)
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
	if got := toolSummary(raw, "other"); got != strings.Repeat("界", 1024)+"…" {
		t.Fatal("invalid truncation length", len(got))
	}
}
