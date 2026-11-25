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

func TestChildAndCompactionPeer(t *testing.T) {
	if os.Getenv("ACP_CHILD_FIXTURE") != "1" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(row map[string]any) {
		row["jsonrpc"] = "2.0"
		if enc.Encode(row) != nil {
			os.Exit(1)
		}
	}
	update := func(id string, body map[string]any) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": id, "update": body}})
	}
	announce := func(parent, child, title string, canCancel bool) {
		caps := map[string]any{}
		if canCancel {
			caps["cancel"] = map[string]any{}
		}
		update(parent, map[string]any{"sessionUpdate": "subagent_update", "sessionId": child, "title": title, "capabilities": caps})
	}
	text := func(id, value string) {
		update(id, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": value}})
	}
	compact := func() {
		update("root", map[string]any{"sessionUpdate": "notice", "title": "Live only", "message": "do not replay"})
		update("root", map[string]any{"sessionUpdate": "compaction_update", "compactionId": "c1", "status": "in_progress"})
		update("root", map[string]any{"sessionUpdate": "compaction_summary_chunk", "compactionId": "c1",
			"content": map[string]string{"type": "text", "text": "partial"}})
		update("root", map[string]any{"sessionUpdate": "compaction_update", "compactionId": "c1", "status": "completed",
			"summary": []map[string]string{{"type": "text", "text": "replacement"}}})
	}
	in := bufio.NewScanner(os.Stdin)
	var turn json.RawMessage
	for in.Scan() {
		var req struct {
			ID     json.RawMessage
			Method string
			Params struct {
				SessionID          string
				ClientCapabilities struct {
					Subagents *empty
					Session   struct{ Compaction, Notices *empty }
				}
			}
			Result struct{ Outcome struct{ Outcome string } }
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			caps := req.Params.ClientCapabilities
			if caps.Subagents == nil || caps.Session.Compaction == nil || caps.Session.Notices == nil {
				os.Exit(3)
			}
			send(map[string]any{"id": req.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": true}}})
		case "session/new":
			send(map[string]any{"id": req.ID, "result": map[string]string{"sessionId": "root"}})
			text("child", "unverified child activity")
			announce("root", "child", "Child", true)
			announce("child", "grandchild", "Grandchild", false)
			announce("grandchild", "child", "invalid cycle", false)
			text("grandchild", "verified descendant")
			text("unrelated", "outside parent")
			compact()
		case "session/prompt":
			turn = req.ID
			send(map[string]any{"id": 77, "method": "session/request_permission", "params": map[string]any{
				"sessionId": "child", "toolCall": map[string]string{"toolCallId": "tool", "title": "Child permission"},
				"options": []map[string]string{{"optionId": "run", "name": "Run", "kind": "allow_once"}},
			}})
		case "session/cancel":
			if req.Params.SessionID != "child" {
				os.Exit(4)
			}
			announce("root", "child", "Child cancel unavailable", false)
		case "":
			if string(req.ID) != "77" || req.Result.Outcome.Outcome != "cancelled" {
				os.Exit(5)
			}
			send(map[string]any{"id": turn, "result": map[string]string{"stopReason": "end_turn"}})
		case "session/load":
			if req.Params.SessionID != "root" {
				os.Exit(6)
			}
			announce("root", "child", "Recovered child", false)
			text("child", "archived child")
			compact()
			send(map[string]any{"id": req.ID, "result": map[string]any{}})
		default:
			os.Exit(7)
		}
	}
	os.Exit(0)
}

func TestV1ChildrenAreScopedAndCompactionPreservesItsPosition(t *testing.T) {
	rt, err := New(Config{
		HarnessInstance: "children", WorkingDirectory: t.TempDir(), Command: os.Args[0],
		Args: []string{"-test.run=^TestChildAndCompactionPeer$"}, Env: map[string]string{"ACP_CHILD_FIXTURE": "1"},
		ReplayHistory: true, ChildSessions: true, NoticeUpdates: true, CompactionUpdates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := agentacp.New(rt)
	opened, err := client.Open(ctx, &model.OpenRequest{HarnessInstance: "children"})
	if err != nil {
		t.Fatal(err)
	}
	root := opened.GetSession().GetRef()
	if _, err := client.Send(ctx, &model.SendRequest{Session: root, OperationId: "first", Text: "root prompt"}); err != nil {
		t.Fatal(err)
	}
	var descendant, denied, notice, completed bool
	var position uint64
	for row, err := range client.Observe(ctx, &model.ObserveRequest{Session: root}) {
		if err != nil {
			t.Fatal(err)
		}
		ext := row.GetExtension()
		if row.GetInput() != nil || strings.Contains(string(ext.GetData()), "invalid cycle") ||
			strings.Contains(string(ext.GetData()), "unverified child activity") || strings.Contains(string(ext.GetData()), "outside parent") {
			t.Fatalf("unverified or actionable child crossed into parent: %v", row)
		}
		switch ext.GetType() {
		case ACPChildUpdateSource:
			if strings.Contains(string(ext.GetData()), "verified descendant") && row.GetSession().GetSessionId() == "root" && row.GetTool() == nil {
				descendant = true
			}
		case ACPChildPermissionSource:
			denied = strings.Contains(string(ext.GetData()), "Child permission")
		case "notice":
			notice = strings.Contains(string(ext.GetData()), "Live only")
		case ACPCompactionStateSource:
			var state compaction
			if err := json.Unmarshal(ext.GetData(), &state); err != nil {
				t.Fatal(err)
			}
			if position == 0 {
				position = state.First
			}
			if state.First != position || state.First == 0 || row.GetSourceRecord().GetType() != ACPUpdateParamsSource {
				t.Fatalf("compaction moved or lost its original: %v", row)
			}
			if state.Status == "completed" {
				completed = len(state.Summary) == 1 && strings.Contains(string(state.Summary[0]), "replacement")
			}
		}
		if row.GetTurnEnded() != nil {
			break
		}
	}
	if !descendant || !denied || !notice || !completed {
		t.Fatalf("missing child or compaction evidence: descendant=%v denied=%v notice=%v completed=%v", descendant, denied, notice, completed)
	}
	roster, err := rt.ChildSessions(root)
	if err != nil || len(roster) != 2 || roster[0].ParentID != "root" || !roster[0].CanCancel || roster[1].ParentID != "child" || roster[1].CanCancel {
		t.Fatalf("wrong child roster: %+v %v", roster, err)
	}
	if err := rt.CancelChild(ctx, root, "grandchild"); err == nil {
		t.Fatal("cancellation permitted for child without a capability")
	}
	if _, err := client.Resume(ctx, &model.ResumeRequest{Session: &model.SessionRef{HarnessInstance: "children", SessionId: "child"}}); err == nil {
		t.Fatal("child was reattached directly")
	}
	if err := rt.CancelChild(ctx, root, "child"); err != nil {
		t.Fatal(err)
	}
	for row, err := range client.Observe(ctx, &model.ObserveRequest{Session: root}) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(row.GetExtension().GetData()), "Child cancel unavailable") {
			if err := rt.CancelChild(ctx, root, "child"); err == nil {
				t.Fatal("obsolete capability still allowed child cancellation")
			}
			return
		}
	}
	t.Fatal("child capability revocation was not observed")
}

func TestV1ReplayKeepsChildAndCompactionButDropsNotice(t *testing.T) {
	rt, err := New(Config{
		HarnessInstance: "children", WorkingDirectory: t.TempDir(), Command: os.Args[0],
		Args: []string{"-test.run=^TestChildAndCompactionPeer$"}, Env: map[string]string{"ACP_CHILD_FIXTURE": "1"},
		ReplayHistory: true, ChildSessions: true, NoticeUpdates: true, CompactionUpdates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := &model.SessionRef{HarnessInstance: "children", SessionId: "root"}
	snapshot, err := rt.LoadReplay(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := snapshot.Page("", 32)
	if err != nil {
		t.Fatal(err)
	}
	child, compact := false, false
	for _, row := range page.GetObservations() {
		ext := row.GetExtension()
		if strings.Contains(string(ext.GetData()), "Live only") {
			t.Fatalf("passive history included a live-only notice: %v", row)
		}
		if ext.GetType() == ACPChildUpdateSource && strings.Contains(string(ext.GetData()), "archived child") {
			child = true
		}
		if ext.GetType() == ACPCompactionStateSource && strings.Contains(string(ext.GetData()), "replacement") {
			compact = true
		}
	}
	if !child || !compact {
		t.Fatalf("passive history lost a child or compaction: child=%v compaction=%v", child, compact)
	}
}
