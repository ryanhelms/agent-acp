package broker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedPeerFixture(t *testing.T) {
	countPath := os.Getenv("ACP_BROKER_FIXTURE")
	if countPath == "" {
		return
	}
	file, err := os.OpenFile(countPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(1)
	}
	_, _ = file.WriteString("process\n")
	_ = file.Close()
	out := json.NewEncoder(os.Stdout)
	write := func(row map[string]any) {
		row["jsonrpc"] = "2.0"
		if out.Encode(row) != nil {
			os.Exit(2)
		}
	}
	active := map[string]bool{}
	var promptID json.RawMessage
	var promptedSession string
	opens := 0
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var req struct {
			Method string
			ID     json.RawMessage
			Params struct{ SessionID string }
			Result struct {
				Outcome struct{ Outcome, OptionID string }
			}
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			os.Exit(3)
		}
		switch req.Method {
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]int{"protocolVersion": 2}})
		case "session/new":
			opens++
			id := fmt.Sprintf("session-%d", opens)
			active[id] = true
			write(map[string]any{"id": req.ID, "result": map[string]string{"sessionId": id}})
			write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": id, "update": map[string]string{"sessionUpdate": "state_update", "state": "idle"}}})
		case "session/prompt":
			if !active[req.Params.SessionID] {
				os.Exit(4)
			}
			write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": req.Params.SessionID, "update": map[string]string{"sessionUpdate": "agent_message", "messageId": req.Params.SessionID}}})
			write(map[string]any{"id": 99, "method": "session/request_permission", "params": map[string]any{"sessionId": req.Params.SessionID, "title": "Request"}})
			promptID, promptedSession = req.ID, req.Params.SessionID
		case "":
			if string(req.ID) != "99" || req.Result.Outcome.Outcome != "selected" || req.Result.Outcome.OptionID != "allow" {
				os.Exit(6)
			}
			write(map[string]any{"id": promptID, "result": map[string]string{"messageId": promptedSession}})
		case "session/close":
			delete(active, req.Params.SessionID)
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		default:
			os.Exit(5)
		}
	}
	os.Exit(0)
}

func TestBrokerSharesPeerAcrossSocketsAndContainsCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("/tmp", "acpb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	processTracker := filepath.Join(dir, "starts")
	pool, err := Listen(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(exe)
	scope := Scope{
		Command: path, WorkingDirectory: dir, ExecutableSHA256: hex.EncodeToString(digest[:]),
		PolicyIdentity: "test-policy", CredentialIdentity: "profile-a", ProtocolVersion: 2,
		Args: []string{"-test.run=^TestSharedPeerFixture$"}, Env: []string{"ACP_BROKER_FIXTURE=" + processTracker},
	}

	type connection struct {
		conn    net.Conn
		decoder *json.Decoder
		encoder *json.Encoder
		session string
	}
	send := func(c connection, id int, method string, params any) {
		t.Helper()
		if err := c.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	recv := func(c connection) frame {
		t.Helper()
		var output frame
		if err := c.decoder.Decode(&output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	launch := func(scope Scope, workerCredential string, prewarm bool) connection {
		t.Helper()
		setup := map[string]any{"cwd": dir, "mcpServers": []map[string]string{{"testWorkerCredential": workerCredential}}}
		ticket, err := pool.Register(Launch{Scope: scope, New: mustJSON(setup)})
		if err != nil {
			t.Fatal(err)
		}
		if prewarm {
			if err := pool.Prewarm(ticket); err != nil {
				t.Fatal(err)
			}
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(6 * time.Second))
		if _, err := fmt.Fprint(conn, ticket); err != nil {
			t.Fatal(err)
		}
		lane := connection{conn: conn, decoder: json.NewDecoder(conn), encoder: json.NewEncoder(conn)}
		send(lane, 1, "initialize", map[string]int{"protocolVersion": 2})
		if response := recv(lane); string(response.ID) != "1" || response.Error != nil {
			t.Fatalf("initialize: %+v", response)
		}
		send(lane, 2, "session/new", setup)
		opened := recv(lane)
		var result struct{ SessionID string }
		if string(opened.ID) != "2" || opened.Error != nil || json.Unmarshal(opened.Result, &result) != nil || result.SessionID == "" {
			t.Fatalf("open failed: %+v", opened)
		}
		lane.session = result.SessionID
		if event := recv(lane); event.Method != "session/update" || !strings.Contains(string(event.Params), lane.session) {
			t.Fatalf("setup update did not reach its runner: %+v", event)
		}
		return lane
	}
	first := launch(scope, "worker-a", true)
	second := launch(scope, "worker-b", false)
	if first.session != "session-1" || second.session != "session-2" {
		t.Fatalf("compatible workers did not share one peer: %q %q", first.session, second.session)
	}
	send(first, 3, "session/prompt", map[string]string{"sessionId": second.session})
	if crossed := recv(first); crossed.Error == nil {
		t.Fatal("cross-worker prompt accepted")
	}
	send(second, 3, "session/prompt", map[string]string{"sessionId": second.session})
	if update := recv(second); update.Method != "session/update" || !strings.Contains(string(update.Params), second.session) {
		t.Fatalf("update crossed sessions: %+v", update)
	}
	request := recv(second)
	if string(request.ID) != "99" || request.Method != "session/request_permission" || !strings.Contains(string(request.Params), second.session) {
		t.Fatalf("permission crossed sessions: %+v", request)
	}
	reply := func(c connection, option string) {
		if err := c.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "result": map[string]any{
			"outcome": map[string]string{"outcome": "selected", "optionId": option},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	reply(first, "crossed")
	send(first, 4, "session/close", map[string]string{"sessionId": first.session})
	if closed := recv(first); closed.Error != nil {
		t.Fatalf("close: %+v", closed)
	}
	reply(second, "allow")
	if prompted := recv(second); string(prompted.ID) != "3" || prompted.Error != nil {
		t.Fatalf("prompt response: %+v", prompted)
	}
	scope.CredentialIdentity = "profile-b"
	different := launch(scope, "worker-c", false)
	if different.session != "session-1" {
		t.Fatalf("a different credential scope improperly reused a peer: %+v", different)
	}
	starts, err := os.ReadFile(processTracker)
	if err != nil || string(starts) != "process\nprocess\n" {
		t.Fatalf("want two provider processes for three runners, got %q (%v)", starts, err)
	}
}
