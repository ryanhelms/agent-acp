package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"agent-acp.local/transport"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const wireTestParams = `{"sessionId":"selected","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"chat"}}}`
const wireTestExtras = `{"_meta":{"n":9007199254740993},"future":[null,"envelope"]}`
const wireTestPermission = `{"sessionId":"selected","_meta":{"n":"payload"},"toolCall":{"title":"Approve?"},"options":[{"optionId":"ok","name":"yes"}]}`
const wireTestResult = `{"stopReason":"end_turn","_meta":{"where":"result"}}`
const wireTestID = `9007199254740993`

// A real newline-delimited peer. It deliberately uses IDs and properties that
// a generic float64/object round trip would change or drop.
func TestWireTestPeer(t *testing.T) {
	if os.Getenv("ACP_WIRE_TEST_PEER") != "1" {
		return
	}
	out := func(frame string) { fmt.Fprintln(os.Stdout, frame) }
	notify := func(params, extras string) {
		members := ""
		if extras != "" {
			members = "," + extras[1:len(extras)-1]
		}
		out(`{"jsonrpc":"2.0","method":"session/update","params":` + params + members + `}`)
	}
	var promptID string
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var m struct {
			Method     string
			ID, Params json.RawMessage
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			os.Exit(1)
		}
		id := string(m.ID)
		reply := func(body string) { out(`{"jsonrpc":"2.0","id":` + id + `,"result":` + body + `}`) }
		switch m.Method {
		case "initialize":
			notify(wireTestParams, `{"private":"setup"}`)
			reply(`{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
		case "session/new":
			reply(`{"sessionId":"selected"}`)
		case "session/load":
			notify(strings.Replace(wireTestParams, "selected", "foreign", 1), `{"private":"wrong"}`)
			notify(wireTestParams, wireTestExtras)
			notify(strings.Replace(wireTestParams, "chat", "second", 1), wireTestExtras)
			reply("{}")
		case "session/prompt":
			var p struct{ Prompt []struct{ Text string } }
			if json.Unmarshal(m.Params, &p) != nil || len(p.Prompt) != 1 {
				os.Exit(2)
			}
			switch value := p.Prompt[0].Text; value {
			case "mixed":
				promptID = id
				notify(strings.Replace(wireTestParams, "selected", "foreign", 1), `{"private":"wrong"}`)
				notify(wireTestParams, "")
				notify(wireTestParams, wireTestExtras)
				out(`{"jsonrpc":"2.0","method":"session/request_permission","id":` + wireTestID + `,"params":` + wireTestPermission + `,"_meta":{"n":9007199254740993}}`)
			case "null-id":
				promptID = id
				out(`{"jsonrpc":"2.0","id":null,"method":"session/update","params":` + wireTestParams + `}`)
			case "disconnect":
				os.Exit(0)
			case "result-close":
				reply(wireTestResult)
				os.Exit(0)
			default:
				out(`{"jsonrpc":"2.0","id":` + id + `,"error":` + wireTestError(value) + `,"_meta":false}`)
			}
		case "":
			if id == wireTestID && !bytes.Contains(in.Bytes(), []byte(`"optionId":"ok"`)) {
				os.Exit(3)
			}
			if id == wireTestID || id == "null" {
				out(`{"jsonrpc":"2.0","id":` + promptID + `,"result":` + wireTestResult + `,"_meta":"wire"}`)
			}
		}
	}
	os.Exit(0)
}

func wireTestError(code string) string {
	return `{ "message":"peer error", "code":` + code + `, "unknown":null, "data":{"exact":9007199254740993}, "_meta":{"more":true} }`
}

func wireTestRuntime(t *testing.T, cb func(ACPUpdate)) (*Runtime, *agentacp.Sessions, context.Context) {
	t.Helper()
	r, err := New(Config{HarnessInstance: "wire", Command: os.Args[0], Args: []string{"-test.run=^TestWireTestPeer$"}, WorkingDirectory: t.TempDir(), ReplayHistory: true, Env: map[string]string{"ACP_WIRE_TEST_PEER": "1"}, OnACPUpdate: cb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(cancel)
	return r, agentacp.New(r), ctx
}

func checkWire(t *testing.T, o *model.Observation, kind model.WireMessageKind, method, id, extras, body string) {
	t.Helper()
	w := o.WireProvenance
	if w.GetKind() != kind || w.GetProtocolVersion() != "2.0" || w.GetMethod() != method || (id == "") != (w.IdJson == nil) || w.GetIdJson() != id || w.GetAdditionalFieldsJson() != extras {
		t.Fatalf("wrong wire metadata: %v", w)
	}
	if o.GetSourceCursor() == "" || o.GetSourceCursor() != o.Cursor || o.GetSession().GetSessionId() != "selected" || string(o.GetSourceRecord().GetData()) != body {
		t.Fatalf("wrong scoped source at %q: %v", o.Cursor, o.GetSourceRecord())
	}
}

func TestWireProvenanceThroughSessionAndReplay(t *testing.T) {
	updates := make(chan ACPUpdate, 4)
	r, c, ctx := wireTestRuntime(t, func(u ACPUpdate) { updates <- u })
	ref := &model.SessionRef{HarnessInstance: "wire", SessionId: "selected"}
	page, err := c.ReadHistory(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 1})
	if err != nil || len(page.GetObservations()) != 1 || page.NextCursor == "" {
		t.Fatalf("replay: %v %v", err, page)
	}
	checkWire(t, page.Observations[0], model.WireMessageKind_WIRE_MESSAGE_KIND_NOTIFICATION, "session/update", "", wireTestExtras, wireTestParams)
	next, err := c.ReadHistory(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.GetObservations()) != 1 || next.NextCursor != "" {
		t.Fatalf("replay continuation: %v %v", err, next)
	}
	checkWire(t, next.Observations[0], model.WireMessageKind_WIRE_MESSAGE_KIND_NOTIFICATION, "session/update", "", wireTestExtras, strings.Replace(wireTestParams, "chat", "second", 1))
	open, err := c.Open(ctx, &model.OpenRequest{HarnessInstance: "wire"})
	if err != nil {
		t.Fatal(err)
	}
	ref = open.Session.Ref
	stream := observe(t, r, ctx, ref, "")
	send(t, c, ctx, ref, "one", "mixed")
	if o := recv(t, stream); o.GetText().GetText() != "mixed" || o.WireProvenance != nil {
		t.Fatal("setup or foreign evidence reached the active session", o)
	}
	if o := recv(t, stream); o.GetText().GetText() != "chat" || o.WireProvenance != nil || o.SourceRecord != nil {
		t.Fatal("ordinary notification captured extra data", o)
	}
	o := recv(t, stream)
	checkWire(t, o, model.WireMessageKind_WIRE_MESSAGE_KIND_NOTIFICATION, "session/update", "", wireTestExtras, wireTestParams)
	if u := <-updates; u.WireProvenance != nil || u.ParamsSize != 0 {
		t.Fatal("ordinary callback record gained a source")
	}
	u := <-updates
	if !proto.Equal(u.WireProvenance, o.WireProvenance) || u.Cursor != o.Cursor || string(u.Params) != wireTestParams {
		t.Fatal("enriched callback and observation disagree")
	}
	u.WireProvenance.AdditionalFieldsJson = "mutated"
	q := recv(t, stream)
	checkWire(t, q, model.WireMessageKind_WIRE_MESSAGE_KIND_REQUEST, "session/request_permission", wireTestID, `{"_meta":{"n":9007199254740993}}`, wireTestPermission)
	_, err = c.Answer(ctx, &model.AnswerRequest{Session: ref, RequestId: q.GetInput().GetRequestId(), OperationId: "answer", Answer: &model.AnswerRequest_OptionId{OptionId: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	end := recv(t, stream)
	id := end.GetWireProvenance().GetIdJson()
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		t.Fatal("missing prompt ID", id)
	}
	checkWire(t, end, model.WireMessageKind_WIRE_MESSAGE_KIND_RESULT, "session/prompt", id, `{"_meta":"wire"}`, wireTestResult)
	for _, code := range []string{"-32603", "-32800", "-32099"} {
		send(t, c, ctx, ref, "op"+code, code)
		recv(t, stream)
		e := recv(t, stream)
		checkWire(t, e, model.WireMessageKind_WIRE_MESSAGE_KIND_ERROR, "session/prompt", e.GetWireProvenance().GetIdJson(), `{"_meta":false}`, wireTestError(code))
		if e.GetTurnEnded().GetReason() != "adapter returned a prompt error" {
			t.Fatal("peer error hidden", e)
		}
	}
	send(t, c, ctx, ref, "lost", "disconnect")
	recv(t, stream)
	lost := recv(t, stream)
	if lost.WireProvenance != nil || lost.SourceRecord != nil || lost.GetTurnEnded().GetReason() != "adapter disconnected" {
		t.Fatal("synthesized peer evidence", lost)
	}
}

func TestWireMatchedResultAtEOF(t *testing.T) {
	r, c, ctx := wireTestRuntime(t, nil)
	open, err := c.Open(ctx, &model.OpenRequest{HarnessInstance: "wire"})
	if err != nil {
		t.Fatal(err)
	}
	stream := observe(t, r, ctx, open.Session.Ref, "")
	send(t, c, ctx, open.Session.Ref, "op", "result-close")
	recv(t, stream)
	e := recv(t, stream)
	checkWire(t, e, model.WireMessageKind_WIRE_MESSAGE_KIND_RESULT, "session/prompt", e.GetWireProvenance().GetIdJson(), "", wireTestResult)
}

func TestWireIDNullThroughSelectedUpdate(t *testing.T) {
	r, c, ctx := wireTestRuntime(t, nil)
	open, err := c.Open(ctx, &model.OpenRequest{HarnessInstance: "wire"})
	if err != nil {
		t.Fatal(err)
	}
	stream := observe(t, r, ctx, open.Session.Ref, "")
	send(t, c, ctx, open.Session.Ref, "op", "null-id")
	recv(t, stream)
	checkWire(t, recv(t, stream), model.WireMessageKind_WIRE_MESSAGE_KIND_REQUEST, "session/update", "null", "", wireTestParams)
	if e := recv(t, stream); e.GetTurnEnded().GetReason() != "end_turn" {
		t.Fatal("wrong null-ID response correlation", e)
	}
}

func TestWireMetadataBoundsAndSpelling(t *testing.T) {
	large := strings.Repeat("x", 128)
	for _, tt := range []struct{ id, extra string }{
		{`"string\u002fid"`, `{"_meta":{"where":"outer"},"dup":null,"dup":false}`},
		{"null", `{"huge":"` + large + `"}`},
		{`"` + large + `"`, `{"other":"x"}`},
		{"\"\xff\"", "{\"bad\":\"\xff\"}"},
	} {
		line := `{"jsonrpc":"2.0","id":` + tt.id + `,"method":"session/update","params":{"_meta":"inner"},` + tt.extra[1:]
		w, _ := wireRecord(&transport.Receipt{ID: []byte(tt.id), Envelope: []byte(line)}, "session/update", 100)
		switch {
		case len(tt.id) > 100:
			if w.OmittedIdBytes != uint64(len(tt.id)) || w.IdJson != nil {
				t.Fatal(w)
			}
		case len(tt.extra) > 100:
			if w.OmittedAdditionalFieldsBytes != uint64(len(tt.extra)) || w.AdditionalFieldsJson != "" || w.GetIdJson() != "null" {
				t.Fatal(w)
			}
		case len(w.IdJsonBytes) > 0:
			if string(w.IdJsonBytes) != tt.id || string(w.AdditionalFieldsJsonBytes) != tt.extra || w.IdJson != nil {
				t.Fatal(w)
			}
		default:
			if w.GetIdJson() != tt.id || w.AdditionalFieldsJson != tt.extra {
				t.Fatal(w)
			}
		}
		if _, err := protojson.Marshal(w); err != nil {
			t.Fatal(err)
		}
	}
	s := boundUpdateSession(nil)
	wire := &model.WireProvenance{Method: "session/update", AdditionalFieldsJson: strings.Repeat("a", liveWindowBytes-128), IdJson: proto.String("12")}
	e := s.emitWireSource(nil, ACPUpdateParamsSource, []byte(strings.Repeat("x", 300)), wire)
	if e.WireProvenance.OmittedAdditionalFieldsBytes == 0 || e.WireProvenance.GetIdJson() != "12" || len(e.GetExtension().GetData()) != 300 || s.windowBytes > liveWindowBytes {
		t.Fatal("combined bounds lost the original or small ID")
	}
}

func TestWireScopeDoesNotWaitForSessionLock(t *testing.T) {
	s := newSession()
	if s.selectWire("session/update", "selected") {
		t.Fatal("setup accepted")
	}
	id := "selected"
	s.wireSession.Store(&id)
	s.mu.Lock()
	done := make(chan bool, 1)
	go func() {
		ok := s.selectWire("session/update", id) && !s.selectWire("session/update", "foreign") &&
			!s.selectWire("session/request_permission", id) && !s.selectWire("setup", id)
		s.wireTurn.Store(true)
		ok = ok && s.selectWire("session/request_permission", id)
		s.wireTurn.Store(false)
		ok = ok && !s.selectWire("session/request_permission", id)
		s.wireSession.Store(nil)
		done <- ok && !s.selectWire("session/update", id)
	}()
	defer s.mu.Unlock()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("scope changed outside accepted session or turn")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transport reader blocked on session lock")
	}
}

func TestOversizedTypedWireRetainsTerminalOrPassiveSource(t *testing.T) {
	s := boundUpdateSession(nil)
	huge := strings.Repeat("x", liveWindowBytes+64)
	for _, event := range []*model.Observation{
		{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: huge}}},
		{Event: &model.Observation_Input{Input: &model.InputRequest{RequestId: "abc", Choices: []*model.Choice{{Id: huge}}}}},
	} {
		wasEnd := event.GetTurnEnded() != nil
		e := s.emitWireSource(event, "acp.oversize", []byte(huge), &model.WireProvenance{ProtocolVersion: proto.String("2.0")})
		if e.GetSourceCursor() != e.Cursor || s.windowBytes > liveWindowBytes || (wasEnd && e.GetTurnEnded().GetReason() != "adapter returned an oversized terminal reason") || (!wasEnd && e.GetExtension().GetType() != "acp.source_omitted") {
			t.Fatal("oversized typed evidence disappeared")
		}
	}
}
