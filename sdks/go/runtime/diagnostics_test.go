package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"

	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

const (
	startupMarker = "private-before-initialize"
	setupMarker   = "private-connector-setup"
	otherMarker   = "private-other-session"
	idMarker      = "private-transport-id"
	childMessage  = "adapter diagnostic on genuine stderr"
	sourceMarker  = "retained-selected-session"
)

// The subprocess writes before initialize as well as after the session is bound.
func TestDiagnosticPeerProcess(t *testing.T) {
	if os.Getenv("TEST_ACP_DIAGNOSTIC_PEER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, `{"jsonrpc":, "marker":"`+startupMarker+`"}`)
	fmt.Fprintln(os.Stderr, childMessage)
	write := json.NewEncoder(os.Stdout).Encode
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage
			Method string
			Params json.RawMessage
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			// A bad unaddressed frame includes the real supplied session setup.
			_ = write(map[string]any{"jsonrpc": "2.0", "params": request.Params})
			result = map[string]string{"sessionId": "selected"}
		case "session/prompt":
			// A malformed cancellation ID is logged separately from the raw-frame attribute.
			_ = write(map[string]any{"jsonrpc": "2.0", "method": "$/cancel_request", "params": map[string]any{"requestId": map[string]string{"marker": idMarker}}})
			for _, row := range [][2]string{{"other", otherMarker}, {"selected", sourceMarker}} {
				params := map[string]any{"sessionId": row[0], "_meta": map[string]string{"marker": row[1]},
					"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": row[1]}}}
				_ = write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": params})
			}
			result = map[string]string{"stopReason": "end_turn"}
		default:
			os.Exit(3)
		}
		if err := write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func TestTransportDiagnosticsKeepWireDataOutOfStderr(t *testing.T) {
	diagnostics, err := os.CreateTemp(t.TempDir(), "diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { diagnostics.Close() })
	defaultOutput, err := os.CreateTemp(t.TempDir(), "default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { defaultOutput.Close() })
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(defaultOutput, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	r, c, ctx := newTestRuntime(t)
	r.config.Args = []string{"-test.run=^TestDiagnosticPeerProcess$"}
	r.config.Env = map[string]string{"TEST_ACP_DIAGNOSTIC_PEER": "1"}
	r.config.Stderr = diagnostics
	r.config.SessionMeta = map[string]any{"credential": setupMarker}
	opened, err := c.Open(ctx, &model.OpenRequest{HarnessInstance: "unit"})
	if err != nil {
		t.Fatal(err)
	}
	ref := opened.Session.Ref
	s := observe(t, r, ctx, ref, "")
	send(t, c, ctx, ref, "test-turn", "read")
	var records []byte
	for {
		o := recv(t, s)
		data, err := proto.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, data...)
		if o.GetText().GetText() == sourceMarker && !bytes.Contains(o.GetSourceRecord().GetData(), []byte(sourceMarker)) {
			t.Fatal("selected-session typed text lost its original metadata")
		}
		if o.GetTurnEnded() != nil {
			break
		}
	}
	if !bytes.Contains(records, []byte(sourceMarker)) {
		t.Fatal("selected-session source missing")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(diagnostics.Name())
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := os.ReadFile(defaultOutput.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logged, []byte(childMessage)) {
		t.Error("genuine child stderr missing")
	}
	for _, kind := range []string{"failed to parse incoming message", "received message with neither id nor method", "failed to canonicalize $/cancel_request requestId"} {
		if !bytes.Contains(logged, []byte(kind)) {
			t.Errorf("sanitized transport diagnostic missing: %s", kind)
		}
	}
	for _, marker := range []string{startupMarker, setupMarker, otherMarker, idMarker} {
		for surface, contents := range map[string][]byte{"configured stderr": logged, "process default": fallback, "observations": records} {
			if bytes.Contains(contents, []byte(marker)) {
				t.Errorf("fixture marker %q disclosed in %s", marker, surface)
			}
		}
	}
}
