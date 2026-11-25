package nativehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	model "agent-acp.local/sdks/go/model"
)

func fixture(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func pages(t *testing.T, provider, dir string, limit uint32) []*model.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	read := ForHarness(provider, dir)
	req := &model.ReadHistoryRequest{Session: &model.SessionRef{HarnessInstance: "local", SessionId: "fixture"}, Limit: limit}
	var all []*model.Observation
	seen := map[string]bool{}
	for {
		res, err := read(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Observations) > int(limit) {
			t.Fatal("page exceeds limit")
		}
		for _, event := range res.Observations {
			if seen[event.Cursor] || event.Cursor == "" || event.Session.GetSessionId() != "fixture" || event.Session.GetHarnessInstance() != "local" {
				t.Fatalf("invalid event identity: %v", event)
			}
			if event.GetInput() != nil {
				t.Fatal("history contains an actionable request")
			}
			seen[event.Cursor] = true
			all = append(all, event)
		}
		if res.NextCursor == "" {
			break
		}
		if req.Cursor == res.NextCursor {
			t.Fatal("cursor did not advance")
		}
		bad := &model.ReadHistoryRequest{Session: &model.SessionRef{HarnessInstance: "different", SessionId: "fixture"}, Cursor: res.NextCursor}
		if _, err := read(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid history cursor") {
			t.Fatalf("cross-session cursor: %v", err)
		}
		req.Cursor = res.NextCursor
	}
	return all
}

func TestClaudeHistory(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Claude reader requires Node")
	}
	dir := t.TempDir()
	t.Setenv("AGENT_ACP_HARNESS_DIR", dir)
	t.Setenv("HISTORY_FIXTURE_CWD", dir)
	module := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk")
	fixture(t, filepath.Join(module, "package.json"), `{"type":"module","exports":"./sdk.mjs"}`, 0644)
	fixture(t, filepath.Join(module, "sdk.mjs"), `
export async function getSessionMessages(id, options) {
  if (id!=="fixture" || options.dir!==process.env.HISTORY_FIXTURE_CWD || options.includeSystemMessages !== true) throw Error("scope mismatch");
  const history = [{type:"user",message:{content:"\tHello $world\n"}}, {type:"assistant",uuid:"assistant-uuid",msgField:{retained:true},message:{content:[
    {type:"text",text:"ready"},{type:"tool_use",name:"Read",id:"call1",input:{path:"demo"},futureNative:{marker:"claude-source-only"}},
    {type:"permission",request:"old request"}]}}, {type:"user",message:{content:[{type:"tool_result",tool_use_id:"call1",content:"done"}]}}];
  for (let i=0;i<22;i++) history.push({type:"system", message:{content:String(i)}});
  return history.slice(options.offset,options.offset+options.limit);
}
`, 0644)
	events := pages(t, "claude", dir, 2)
	if len(events) != 27 || events[0].GetText().GetText() != "\tHello $world\n" || events[1].GetText().GetText() != "ready" || events[2].GetTool().GetCallId() != "call1" || events[3].GetExtension().GetType() != "claude.permission" || events[4].GetTool().GetSummary() != "done" || events[26].GetText().GetText() != "21" {
		t.Fatalf("incorrect Claude history: %v", events)
	}
	for _, item := range events[1:4] {
		if item.GetSourceCursor() != events[1].Cursor {
			t.Fatal("normalized Claude block is not linked to its original message")
		}
	}
	if events[2].SourceRecord != nil || events[3].SourceRecord != nil {
		t.Fatal("Claude duplicated the message source across a page boundary")
	}
	source := events[1].GetSourceRecord()
	if source.GetType() != "claude.history.message" || !strings.Contains(string(source.GetData()), "assistant-uuid") ||
		!strings.Contains(string(source.GetData()), "claude-source-only") {
		t.Fatal("lost Claude's native envelope or unknown field on a known tool")
	}
}

func TestCodexHistory(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("test app server fixture requires Node")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	t.Setenv("CODEX_PATH", bin)
	fixture(t, bin, `#!/usr/bin/env node
const readline = require("node:readline").createInterface({input:process.stdin});
for (const flag of ["app-server","--listen","stdio://"]) if (!process.argv.includes(flag)) process.exit(1);
readline.on("line", line => {
  const {method,id,params}=JSON.parse(line);
  let result;
  switch(method) {
    case "initialize": result={}; break;
    case "initialized": return;
    case "thread/read":
      if (params.threadId!=="fixture" || params.includeTurns!==false) process.exit(2);
      result={thread:{id:params.threadId}}; break;
    case "thread/turns/list":
      if (params.threadId!=="fixture" || params.sortDirection!=="asc" || params.itemsView!=="full") process.exit(3);
      if (!params.cursor) {
        result={data:[{id:"turn1",itemsView:"full",status:"completed",futureTurn:{marker:"turn-source-only"},items:[
          {type:"userMessage",content:[{type:"text",text:"\tHello $world\n"},{type:"image",url:"fixture"}]},
          {type:"commandExecution",id:"run",command:"echo hi",aggregatedOutput:"hi\n",status:"completed",exitCode:7,durationMs:251,futureNative:{marker:"codex-source-only"}},
          {type:"agentMessage",text:"hi",questions:[{title:"Retry?",options:["yes","no"]}]}]}],nextCursor:"opaque native cursor"};
      } else if (params.cursor==="opaque native cursor") {
        result={data:[{id:"turn2",itemsView:"full",status:"inProgress",items:[{type:"permission",id:"old"}]}]};
      } else process.exit(4);
      break;
    default: process.exit(5); // a history read must never activate a thread
  }
  process.stdout.write(JSON.stringify({id,result})+"\n");
});
`, 0755)
	events := pages(t, "codex", dir, 2)
	if len(events) != 9 || events[0].GetText().GetText() != "\tHello $world\n" || events[1].GetExtension().GetType() != "codex.user.image" || events[2].GetTool().GetSummary() != "echo hi\nhi\n" || events[3].GetText().GetText() != "hi" || events[4].GetExtension().GetType() != "codex.agentMessage.questions" || events[5].GetTurnEnded().GetReason() != "completed" || events[6].GetExtension().GetType() != "codex.permission" || events[6].GetTurnId() != "turn2" || events[8].GetExtension().GetType() != "codex.history.thread" {
		t.Fatalf("incorrect Codex history: %v", events)
	}

	var command struct {
		ID                   string
		ExitCode, DurationMs int
		FutureNative         struct{ Marker string }
	}
	record := events[2].GetSourceRecord()
	if err := json.Unmarshal(record.GetData(), &command); err != nil || events[2].GetSourceCursor() != events[2].Cursor ||
		record.GetType() != "codex.history.item" || command.ID != "run" || command.ExitCode != 7 || command.DurationMs != 251 ||
		command.FutureNative.Marker != "codex-source-only" {
		t.Fatalf("Codex item fields did not survive: %v %+v", err, command)
	}
	if events[0].GetSourceCursor() != events[1].GetSourceCursor() || events[1].SourceRecord != nil {
		t.Fatal("multipart Codex user message lost its shared original")
	}
	// questions already carry the original item on the following page.
	if events[3].GetSourceCursor() != events[4].Cursor || events[3].SourceRecord != nil || events[4].SourceRecord != nil {
		t.Fatal("Codex dropped or duplicated the addressed historical question source")
	}
	meta := string(events[5].GetSourceRecord().GetData())
	if !strings.Contains(meta, "turn-source-only") || strings.Contains(meta, "codex-source-only") ||
		events[7].GetExtension().GetType() != "codex.history.turn" || events[7].GetSourceCursor() != events[7].Cursor {
		t.Fatal("missing completed or unfinished turn metadata, or duplicated item bytes")
	}
}

func TestHistoryBudgetRetainsAnIndividuallyLargeOriginal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_ACP_HARNESS_DIR", dir)
	module := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk")
	fixture(t, filepath.Join(module, "package.json"), `{"type":"module","exports":"./sdk.mjs"}`, 0644)
	fixture(t, filepath.Join(module, "sdk.mjs"), `
export async function getSessionMessages() {
  return ["first", "large", "last"].map((text) => ({
    type:"user", message:{content:text}, detail:text === "large" ? "Z".repeat(1100000) : ""
  }));
}
`, 0644)
	read := ForHarness("claude", dir)
	req := &model.ReadHistoryRequest{Session: &model.SessionRef{HarnessInstance: "local", SessionId: "fixture"}, Limit: 16}
	for i, want := range []string{"first", "large", "last"} {
		page, err := read(context.Background(), req)
		if err != nil || len(page.GetObservations()) != 1 || page.Observations[0].GetText().GetText() != want {
			t.Fatalf("page %d was truncated or advanced past an original: %v %v", i, err, len(page.GetObservations()))
		}
		if i == 1 && len(page.Observations[0].GetSourceRecord().GetData()) < 1100000 {
			t.Fatal("large original was truncated")
		}
		if (page.NextCursor == "") != (i == 2) {
			t.Fatal("incorrect next cursor")
		}
		req.Cursor = page.NextCursor
	}
}

func TestLegacyHistoryPositionSkipsNewSourceOnlyRows(t *testing.T) {
	observations := []*model.Observation{
		extension("codex.history.emptyItem", nil), extension("codex.opaque", nil),
		extension("codex.history.turn", nil), extension("codex.permission", nil),
	}
	for skip, want := range []int{0, 2, 4} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			position := pageCursor{Skip: skip}
			if err := position.seek(observations); err != nil || position.Skip != want || position.Version != 1 {
				t.Fatalf("legacy cursor %d resolved to %v: %v", skip, position, err)
			}
		})
	}
}

func TestCodexCursorDoesNotRetargetAfterTurnGrowth(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("test app server fixture requires Node")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	t.Setenv("CODEX_PATH", bin)
	t.Setenv("HISTORY_FIXTURE_FILE", filepath.Join(dir, "history.json"))
	fixture(t, bin, `#!/usr/bin/env node
const fs = require("node:fs");
require("node:readline").createInterface({input:process.stdin}).on("line", line => {
  const {id,method,params} = JSON.parse(line);
  if (method === "initialized") return;
  let result = {};
  if (method === "thread/read") result = {thread:{id:params.threadId}};
  if (method === "thread/turns/list") {
    if (!params.cursor) {
      const turn = fs.readFileSync(process.env.HISTORY_FIXTURE_FILE, "utf8");
      process.stdout.write('{"id":'+id+',"result":{"data":['+turn+'],"nextCursor":"next-page"}}\n');
      return;
    }
    result = {data:[{id:"later",itemsView:"full",status:"completed",items:[]}]};
  }
  process.stdout.write(JSON.stringify({id,result})+"\n");
});
`, 0755)
	write := func(items string) {
		t.Helper()
		fixture(t, os.Getenv("HISTORY_FIXTURE_FILE"), `{"id":"active","itemsView":"full","status":"inProgress","items":[`+items+`]}`, 0600)
	}
	reader := ForHarness("codex", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ref := &model.SessionRef{HarnessInstance: "local-codex", SessionId: "fixture"}
	read := func(cursor string, limit uint32) *model.ReadHistoryResponse {
		t.Helper()
		p, err := reader(ctx, &model.ReadHistoryRequest{Session: ref, Cursor: cursor, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	const first = `{"type":"agentMessage","id":"a","text":"original","extra":9007199254740993}`
	write(first)
	before := read("", 2)
	if len(before.Observations) != 2 || before.NextCursor == "" {
		t.Fatalf("unexpected initial page: %v", before)
	}
	var anchorPosition pageCursor
	raw, err := base64.RawURLEncoding.DecodeString(before.Observations[0].Cursor)
	if err != nil || json.Unmarshal(raw, &anchorPosition) != nil || anchorPosition.Version != 2 {
		t.Fatal("newly issued source cursor lacks a content witness")
	}
	legacy := pageCursor{Version: 1, Scope: anchorPosition.Scope, Skip: 1}
	resumed := read(legacy.encode(), 1)
	if len(resumed.Observations) != 1 || resumed.Observations[0].Cursor != before.Observations[1].Cursor {
		t.Fatal("v1 resume did not upgrade to the corresponding v2 cursor")
	}

	anchor := before.Observations[1].GetSourceCursor()
	if next := read(before.NextCursor, 1); len(next.Observations) != 1 || next.Observations[0].TurnId != "later" {
		t.Fatalf("unchanged native boundary failed: %v", next)
	}
	write(first + `,{"type":"agentMessage","id":"b","text":"new"}`)
	after := read("", 2)
	if after.Observations[0].Cursor != before.Observations[0].Cursor {
		t.Fatal("append changed the stable prefix")
	}
	if after.Observations[1].GetSourceCursor() == anchor {
		t.Error("previous turn metadata now identifies a different native item")
	}
	for _, cursor := range []string{anchor, before.NextCursor} {
		page := read(cursor, 2)
		if len(page.Observations) != 1 || page.Observations[0].GetGap().GetReason() == "" || page.Observations[0].GetSession().GetSessionId() != ref.SessionId || page.NextCursor != "" {
			t.Errorf("changed prefix must end in a history gap: %v", page)
		}
	}
	page := read(before.Observations[0].Cursor, 1)
	if len(page.Observations) != 1 || page.Observations[0].GetText().GetText() != "new" {
		t.Fatalf("unchanged prefix skipped appended item: %v", page)
	}
	// Change only a provider field the normalized text does not carry.
	write(strings.Replace(first, "9007199254740993", "9007199254740992", 1))
	if page := read(before.Observations[0].Cursor, 1); len(page.Observations) != 1 || page.Observations[0].GetGap() == nil {
		t.Fatal("changed original field was accepted")
	}
}

// One retained original can supply hundreds of display blocks (for example a
// tool result with an inline image). Its bytes enter the witness once per read.
func BenchmarkHistoryPrefixMultipart(b *testing.B) {
	original := bytes.Repeat([]byte("A"), 4<<20)
	for _, count := range []int{1, 512} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			group := make([]*model.Observation, count)
			for i := range group {
				group[i] = text(model.Speaker_SPEAKER_USER, "display")
			}
			withSource("provider.item", original, group)
			b.ReportAllocs()
			b.SetBytes(int64(len(original)))
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				prefix := batchPrefix{hash: sha256.New()}
				for i := range group {
					prefix.through(group, i)
				}
				if prefix.digest == "" {
					b.Fatal("missing witness")
				}
			}
		})
	}
}
