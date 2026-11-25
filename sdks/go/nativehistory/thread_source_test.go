package nativehistory

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	model "agent-acp.local/sdks/go/model"
)

func TestCodexSelectedThreadOriginal(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("test app server fixture requires Node")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	metaFile, turnFile := filepath.Join(dir, "thread.json"), filepath.Join(dir, "turns.json")
	t.Setenv("CODEX_PATH", bin)
	t.Setenv("TEST_THREAD", metaFile)
	t.Setenv("TEST_TURNS", turnFile)
	fixture(t, bin, `#!/usr/bin/env node
const fs = require("node:fs");
require("node:readline").createInterface({input:process.stdin}).on("line", line => {
  const {method,id,params}=JSON.parse(line);
  let raw;
  switch(method) {
    case "initialize": raw="{}"; break;
    case "initialized": return;
    case "thread/read":
      if (params.threadId!=="fixture" || params.includeTurns!==false) process.exit(2);
      raw=fs.readFileSync(process.env.TEST_THREAD,"utf8"); break;
    case "thread/turns/list":
      if (params.threadId!=="fixture" || params.itemsView!=="full") process.exit(3);
      raw=fs.readFileSync(process.env.TEST_TURNS,"utf8"); break;
    default: process.exit(4);
  }
  process.stdout.write('{"id":'+id+',"result":'+raw+'}\n');
});
`, 0755)
	const original = `{"thread":{ "id":"fixture","turns":[],"preview":null,"vendor":{"nested":[true,9007199254740993]} },"resultSibling":{"revision":9.10e+05}}`
	fixture(t, metaFile, original, 0600)
	fixture(t, turnFile, `{"data":[],"nextCursor":null}`, 0600)
	read := ForHarness("codex", dir)
	ref := &model.SessionRef{HarnessInstance: "local", SessionId: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	check := func(cursor string) *model.ReadHistoryResponse {
		t.Helper()
		page, err := read(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 1, Cursor: cursor})
		if err != nil || len(page.GetObservations()) != 1 {
			t.Fatalf("expected one scoped record: %v, %v", page, err)
		}
		return page
	}
	match := func(p *model.ReadHistoryResponse, raw string) {
		t.Helper()
		o := p.Observations[0]
		if p.NextCursor != "" || o.GetSession() != ref || o.GetTurnId() != "" || o.GetExtension().GetType() != "codex.history.thread" || o.GetExtension().GetMediaType() != "application/json" ||
			string(o.GetExtension().GetData()) != raw || o.SourceRecord != nil || o.Cursor == "" || o.GetSourceCursor() != o.Cursor {
			t.Fatalf("thread metadata was lost, repeated or given another identity: %v", o)
		}
	}
	match(check(""), original)
	fixture(t, turnFile, `{"data":[{"id":"turn1","status":"completed","itemsView":"full","items":[]}]}`, 0600)
	first := check("")
	if first.NextCursor == "" || first.Observations[0].GetTurnEnded().GetReason() != "completed" {
		t.Fatal("metadata displaced turn metadata")
	}
	updated := strings.Replace(original, "9.10e+05", "9.20e+05", 1)
	fixture(t, metaFile, updated, 0600)
	match(check(first.NextCursor), updated)
	for _, raw := range []string{
		`{"thread":{"id":"neighbor"},"Thread":{"id":"fixture"}}`, `{"Thread":{"id":"fixture"}}`,
		`{"thread":{"id":"neighbor","ID":"fixture"}}`, `{"thread":{"id":"neighbor","id":"fixture"}}`,
		`{"thread":{"id":"neighbor"},"thread":{"id":"fixture"}}`, `{"thread":{"id":null}}`,
	} {
		fixture(t, metaFile, raw, 0600)
		if p, err := read(ctx, &model.ReadHistoryRequest{Session: ref, Limit: 1}); p != nil || err == nil || !strings.Contains(err.Error(), "different thread") {
			t.Fatalf("unverified source was accepted: %v, %v", p, err)
		}
	}
}

func TestCodexIdentityRejectsAmbiguity(t *testing.T) {
	const input = `{"thread":{"id":"neighbor","i\u0064":"fixture"}}`
	if id := codexIdentityField(codexIdentityField(json.RawMessage(input), "thread"), "id"); id != nil {
		t.Fatalf("duplicate identity was accepted: %s", id)
	}
}
