package runtime

import (
	agentacp "agent-acp.local/sdks/go"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"

	model "agent-acp.local/sdks/go/model"
)

func boundUpdateSession(capture func(ACPUpdate)) *session {
	s := newSession()
	s.info = &model.Session{Ref: &model.SessionRef{HarnessInstance: "fixture", SessionId: "chosen"}}
	s.p = &peer{runtime: &Runtime{config: Config{OnACPUpdate: capture}}}
	s.turn = &turn{id: "turn"}
	return s
}

func TestOuterUpdateSourceUsesSameCursor(t *testing.T) {
	s := boundUpdateSession(nil)
	params := json.RawMessage(`{"sessionId":"chosen","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}},"_meta":null,"future":{"n":9007199254740993}}`)
	_, err := s.handle(context.Background(), "session/update", params)
	if err != nil || len(s.events) != 1 {
		t.Fatalf("update failed: %v", err)
	}
	event := s.events[0]
	record := event.GetSourceRecord()
	if event.GetText().GetText() != "hello" || event.SourceCursor == nil || *event.SourceCursor != event.Cursor || event.TurnId != "turn" || record.GetType() != "acp.session_update_params" || !bytes.Equal(record.GetData(), params) {
		t.Fatalf("lost scoped params at typed cursor %q (source %q)", event.Cursor, record.GetType())
	}
}

func BenchmarkScopedSessionUpdate(b *testing.B) {
	for _, size := range []int{64, 4096} {
		for _, name := range []string{"ordinary", "metadata", "ordinary-callback", "metadata-callback"} {
			b.Run(name+"/"+map[int]string{64: "64", 4096: "4096"}[size], func(b *testing.B) {
				params := `{"sessionId":"chosen","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("a", size) + `"}}`
				if strings.HasPrefix(name, "metadata") {
					params += `,"_meta":{"vendor":null}`
				}
				raw := json.RawMessage(params + "}")
				var capture func(ACPUpdate)
				if strings.HasSuffix(name, "callback") {
					capture = func(ACPUpdate) {}
				}
				s := boundUpdateSession(capture)
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := s.handle(context.Background(), "session/update", raw); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestUpdateParamsIsolationAndCallbackOwnership(t *testing.T) {
	var callbacks []ACPUpdate
	s := boundUpdateSession(func(u ACPUpdate) { callbacks = append(callbacks, u) })
	for _, raw := range []string{
		`{"sessionId":"foreign","update":{"sessionUpdate":"agent_message_chunk"},"_meta":{"sessionId":"chosen"}}`,
		`{"SessionId":"chosen","update":null,"_meta":null}`,
		`{"sessionId":"foreign","sessionId":"chosen","update":{},"_meta":{}}`,
		`{"sessionId":"chosen","update":null,"update":{"sessionUpdate":"plan"},"future":true}`,
		`{"sessionId":"chosen","update":null,`,
	} {
		_, _ = s.handle(context.Background(), "session/update", json.RawMessage(raw))
	}
	if len(callbacks) != 0 || len(s.events) != 0 {
		t.Fatal("out-of-scope or ambiguous notification escaped")
	}

	inner := `{"sessionUpdate":"vendor_future_update","entries":[],"text":"a \"nested\" brace } [ and backslash \\"}`
	enriched := "  { \"upda\\u0074e\" : " + inner + " , \"future\" : [false,2,{\"id\":null}], \"_me\\u0074a\": null, \"sessionId\" : \"cho\\u0073en\"    } \n"
	_, err := s.handle(context.Background(), "session/update", json.RawMessage(enriched))
	if err != nil || len(callbacks) != 1 {
		t.Fatalf("enriched update missing: %v", err)
	}
	u := callbacks[0]
	e := s.events[0]
	if u.ParamsSize != len(enriched) || string(u.Body) != inner || string(u.Params) != enriched || u.Cursor != e.Cursor || u.Session.SessionId != "chosen" || u.TurnID != "turn" {
		t.Fatalf("incorrect callback at %q", u.Cursor)
	}
	if e.GetSourceRecord() != nil || e.GetExtension().GetType() != ACPUpdateParamsSource || string(e.GetExtension().GetData()) != enriched || e.GetSourceCursor() != e.Cursor {
		t.Fatal("opaque update lost or duplicated its params")
	}
	at := bytes.Index(u.Params, []byte(inner))
	if at < 0 || &u.Body[0] != &u.Params[at] {
		t.Fatal("callback holds two copies of the same original")
	}
	u.Body[0] = '!'
	if e.GetExtension().Data[at] != '{' {
		t.Fatal("callback mutated the observation store")
	}
	u.Session.SessionId = "changed"
	if e.GetSession().GetSessionId() != "chosen" {
		t.Fatal("callback mutated session identity")
	}

	for _, tc := range []struct{ outer, wantBody string }{
		{`{"sessionId":"chosen","_meta":null,"update":null}`, "null"},
		{`{"sessionId":"chosen","_meta":false}`, ""},
		{`{"update":42,"sessionId":"chosen","future":true}`, "42"},
	} {
		_, err = s.handle(context.Background(), "session/update", json.RawMessage(tc.outer))
		last := s.events[len(s.events)-1]
		got := callbacks[len(callbacks)-1]
		if err != nil || string(got.Body) != tc.wantBody || last.GetExtension().GetType() != ACPUpdateParamsSource || last.GetInput() != nil || string(last.GetExtension().Data) != tc.outer || last.GetSourceCursor() != last.Cursor {
			t.Fatalf("invalid passive observation for %s", tc.outer)
		}
	}
	plain := `{ "update": {"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{_meta}"}}, "sessionId":"chosen" }`
	_, err = s.handle(context.Background(), "session/update", json.RawMessage(plain))
	last := s.events[len(s.events)-1]
	got := callbacks[len(callbacks)-1]
	if err != nil || last.GetText().GetText() != "{_meta}" || last.SourceCursor != nil || got.Params != nil || got.ParamsSize != 0 || !bytes.Contains(got.Body, []byte("agent_message_chunk")) {
		t.Fatal("ordinary update lost the original callback or took the enriched path")
	}
}

func TestEnrichedUpdateBoundAndResumption(t *testing.T) {
	var last ACPUpdate
	s := boundUpdateSession(func(u ACPUpdate) { last = u })
	large := `{"sessionId":"chosen","update":{"sessionUpdate":"tool_call","toolCallId":"file","title":"Read"},"_meta":"` + strings.Repeat("x", liveWindowBytes) + `"}`
	_, err := s.handle(context.Background(), "session/update", json.RawMessage(large))
	if err != nil {
		t.Fatal(err)
	}
	e := s.events[0]
	source := e.GetSourceRecord()
	if e.GetTool().GetName() != "Read" || source.GetType() != "acp.source_omitted" || len(source.Data) > 256 || !bytes.Contains(source.Data, []byte("live_window_limit")) || last.Params != nil || last.ParamsSize != len(large) || !bytes.Contains(last.Body, []byte("tool_call")) || s.windowBytes > liveWindowBytes {
		t.Fatal("oversized source suppressed the action or escaped its bound")
	}
	r := s.p.runtime
	r.sessions = map[string]*session{"chosen": s}
	r.config.HarnessInstance = "fixture"
	stream, err2 := r.Stream(context.Background(), "Observe", &model.ObserveRequest{Session: s.info.Ref, AfterCursor: e.Cursor})
	if err2 != nil {
		t.Fatal(err2)
	}
	defer stream.Close()
	_, err = s.handle(context.Background(), "session/update", json.RawMessage(`{"sessionId":"chosen","update":null,"_meta":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	var next model.Observation
	if err := stream.Recv(&next); err != nil || next.Cursor == e.Cursor || next.GetSourceCursor() != next.Cursor || next.GetExtension().GetType() != ACPUpdateParamsSource {
		t.Fatal("source cursor did not resume to the next passive record", err)
	}
	// A semantic preview may itself exceed the window. The compact source gap
	// still has a stable cursor, and older records remain available.
	giant := `{"sessionId":"chosen","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("x", liveWindowBytes) + `"}},"_meta":null}`
	_, err = s.handle(context.Background(), "session/update", json.RawMessage(giant))
	if err != nil || len(s.events) != 3 || s.events[2].GetExtension().GetType() != "acp.source_omitted" || s.events[2].GetSourceCursor() != s.events[2].Cursor || s.windowBytes > liveWindowBytes {
		t.Fatal("oversized preview displaced the live window or its source gap")
	}

}

func TestEnrichedWireObserveAndPagedReplay(t *testing.T) {
	r, c, ctx := newTestRuntime(t)
	r.config.Env["TEST_AGENT_ACP_OUTER_PARAMS"] = "marker"
	ref := opened(t, c, ctx)
	stream := observe(t, r, ctx, ref, "")
	defer stream.Close()
	send(t, c, ctx, ref, "wire", "Hello")
	_ = recv(t, stream)
	got := recv(t, stream)
	check := func(o *model.Observation) {
		t.Helper()
		if o.GetSourceRecord().GetType() != ACPUpdateParamsSource || o.GetSourceCursor() != o.Cursor {
			t.Fatalf("unbound source at %q", o.Cursor)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(o.SourceRecord.Data, &m); err != nil || string(m["future"]) != "9007199254740993" || !bytes.Contains(m["_meta"], []byte(`"nullable":null`)) || string(m["sessionId"]) != "\""+o.Session.SessionId+"\"" {
			t.Fatalf("wire params fields disagree at %q", o.Cursor)
		}
	}
	check(got)
	if got.GetText().GetText() != "Hello" || got.TurnId != "wire" {
		t.Fatal("live typed text lost turn scope")
	}

	cwd := t.TempDir()
	saved := &model.SessionRef{HarnessInstance: "standalone", SessionId: "saved-session"}
	openReplay := func(marker string) *agentacp.Sessions {
		t.Helper()
		rt, err := New(Config{HarnessInstance: saved.HarnessInstance, Command: os.Args[0], Args: []string{"-test.run=^TestAdapterProcess$"},
			WorkingDirectory: cwd, ReplayHistory: true, Env: map[string]string{"TEST_AGENT_ACP_HELPER": "yes", "TEST_AGENT_ACP_REPLAY": "yes",
				"TEST_AGENT_ACP_REPLAY_CWD": cwd, "TEST_AGENT_ACP_OUTER_PARAMS": marker}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		return agentacp.New(rt)
	}
	replay := openReplay("marker")
	var records []*model.Observation
	first := ""
	for cursor := ""; ; {
		page, err := replay.ReadHistory(ctx, &model.ReadHistoryRequest{Session: saved, Cursor: cursor, Limit: 1})
		if err != nil || len(page.GetObservations()) != 1 || len(records) >= 4 {
			t.Fatal("invalid scoped replay", err, len(records))
		}
		records = append(records, page.Observations[0])
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
		if first == "" {
			first = cursor
		}
	}
	if len(records) != 4 || records[0].GetText().GetText() != "literal prompt " || records[1].GetTool().GetPhase() != "tool_call" || records[2].GetTool().GetSummary() != "retained result" || records[3].GetText().GetText() != "Assistant text" {
		t.Fatal("lost typed call, result, or text")
	}
	for _, i := range []int{0, 2, 3} {
		check(records[i])
	}
	if records[1].GetSourceRecord() != nil {
		t.Fatal("synthetic invocation duplicated the original completed tool")
	}
	if _, err := openReplay("changed").ReadHistory(ctx, &model.ReadHistoryRequest{Session: saved, Cursor: first}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("changed outer metadata did not invalidate replay cursor")
	}
}

func TestEnrichedReplayStableOrderAndBound(t *testing.T) {
	paged := func(raw string) *model.Observation {
		page := &replayPage{session: &model.SessionRef{HarnessInstance: "fixture", SessionId: "chosen"}, matched: true, collecting: true,
			prefix: sha256.New(), position: replayCursor{Scope: "fixture-scope"}, limit: 1}
		_, err := page.handle(context.Background(), "session/update", json.RawMessage(raw))
		if err != nil || len(page.response.Observations) != 1 {
			t.Fatalf("invalid page: %v", err)
		}
		return page.response.Observations[0]
	}
	a := `{"update":null,"_meta":{"a":1,"b":false},"sessionId":"chosen"}`
	b := `{ "sessionId":"chosen", "_meta":{ "b":false, "a":1 }, "update":null }`
	x, y := paged(a), paged(b)
	if x.Cursor != y.Cursor || x.GetSourceCursor() != x.Cursor || y.GetSourceCursor() != y.Cursor || string(x.GetExtension().GetData()) != a || string(y.GetExtension().GetData()) != b {
		t.Fatal("replay lost original spelling or stable semantic cursor")
	}
	update := `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"small"}}`
	big := paged(`{"sessionId":"chosen","update":` + update + `,"_meta":"` + strings.Repeat("m", maxReplayRecord) + `"}`)
	if big.GetSourceRecord().GetType() != "acp.source_omitted" || big.GetText().GetText() != "small" || big.GetSourceCursor() != big.Cursor || !bytes.Contains(big.GetSourceRecord().GetData(), []byte("replay_record_limit")) {
		t.Fatal("unbounded outer replay or lost text")
	}
}
