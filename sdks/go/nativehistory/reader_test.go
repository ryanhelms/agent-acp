package nativehistory

import (
	"context"
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
  const history = [{type:"user",message:{content:"\tHello $world\n"}}, {type:"assistant",message:{content:[
    {type:"text",text:"ready"},{type:"tool_use",name:"Read",id:"call1",input:{path:"demo"}},
    {type:"permission",request:"old request"}]}}, {type:"user",message:{content:[{type:"tool_result",tool_use_id:"call1",content:"done"}]}}];
  for (let i=0;i<22;i++) history.push({type:"system", message:{content:String(i)}});
  return history.slice(options.offset,options.offset+options.limit);
}
`, 0644)
	events := pages(t, "claude", dir, 10)
	if len(events) != 27 || events[0].GetText().GetText() != "\tHello $world\n" || events[1].GetText().GetText() != "ready" || events[2].GetTool().GetCallId() != "call1" || events[3].GetExtension().GetType() != "claude.permission" || events[4].GetTool().GetSummary() != "done" || events[26].GetText().GetText() != "21" {
		t.Fatalf("incorrect Claude history: %v", events)
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
        result={data:[{id:"turn1",itemsView:"full",status:"completed",items:[
          {type:"userMessage",content:[{type:"text",text:"\tHello $world\n"},{type:"image",url:"fixture"}]},
          {type:"commandExecution",id:"run",command:"echo hi",aggregatedOutput:"hi\n",status:"completed"},
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
	if len(events) != 7 || events[0].GetText().GetText() != "\tHello $world\n" || events[1].GetExtension().GetType() != "codex.user.image" || events[2].GetTool().GetSummary() != "echo hi\nhi\n" || events[3].GetText().GetText() != "hi" || events[4].GetExtension().GetType() != "codex.agentMessage.questions" || events[5].GetTurnEnded().GetReason() != "completed" || events[6].GetExtension().GetType() != "codex.permission" || events[6].GetTurnId() != "turn2" {
		t.Fatalf("incorrect Codex history: %v", events)
	}
}
