package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
)

func TestMCPACPBindingNegotiatesAndScopesRequests(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/advertised=%t", version, enabled), func(t *testing.T) {
				called := make(chan MCPACPRequest, 3)
				r, err := New(Config{
					HarnessInstance: "mcp", Command: os.Args[0], Args: []string{"-test.run=^TestMCPACPPeer$"},
					WorkingDirectory: t.TempDir(), ProtocolVersion: version,
					Env: map[string]string{"ACP_MCP_FIXTURE": strconv.FormatBool(enabled)},
					MCPServers: []MCPServer{{
						Name: "test", Command: "fixture-stdio",
						CallACP: func(ctx context.Context, req MCPACPRequest) (MCPACPResponse, error) {
							called <- req
							if err := req.Notify(ctx, "notifications/progress", json.RawMessage(`{"progress":1}`)); err != nil {
								return MCPACPResponse{}, err
							}
							if req.RequestID == "blocked" {
								<-ctx.Done()
								return MCPACPResponse{}, ctx.Err()
							}
							return MCPACPResponse{Result: json.RawMessage("null")}, nil
						},
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				opened, err := agentacp.New(r).Open(ctx, &model.OpenRequest{HarnessInstance: "mcp"})
				if err != nil || opened.GetSession().GetRef().GetSessionId() != "mcp-fixture" {
					t.Fatalf("MCP registration handshake failed: %v, %v", opened, err)
				}
				if !enabled {
					select {
					case call := <-called:
						t.Fatalf("unadvertised MCP path received an operation: %+v", call)
					default:
					}
					return
				}
				for _, want := range []string{"literal $HOME", "blocked"} {
					select {
					case call := <-called:
						if call.RequestID != want || call.Method != "tools/call" ||
							string(call.Params) != `{"literalNumber":9007199254740993}` {
							t.Fatalf("logical request changed: %+v", call)
						}
					default:
						t.Fatal("negotiated ACP MCP registration was not called")
					}
				}
				select {
				case extra := <-called:
					t.Fatalf("a notification, foreign session or duplicate caused MCP execution: %+v", extra)
				default:
				}
			})
		}
	}
}

func TestMCPACPPeer(t *testing.T) {
	mode := os.Getenv("ACP_MCP_FIXTURE")
	if mode == "" {
		return
	}
	on := mode == "true"
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	send := func(fields map[string]any) {
		fields["jsonrpc"] = "2.0"
		if encoder.Encode(fields) != nil {
			os.Exit(2)
		}
	}
	receive := func() (frame struct {
		ID     json.RawMessage
		Method string
		Params json.RawMessage
		Result json.RawMessage
		Error  *struct{ Code int }
	}) {
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(3)
		}
		return
	}
	initialize := receive()
	var init struct{ ProtocolVersion int }
	_ = json.Unmarshal(initialize.Params, &init)
	if init.ProtocolVersion == 2 {
		mcp := map[string]any{"stdio": struct{}{}}
		if on {
			mcp["acp"] = struct{}{}
		}
		send(map[string]any{"id": initialize.ID, "result": map[string]any{"protocolVersion": 2, "capabilities": map[string]any{
			"session": map[string]any{"mcp": mcp},
		}}})
	} else {
		send(map[string]any{"id": initialize.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{
			"mcpCapabilities": map[string]bool{"acp": on},
		}}})
	}
	create := receive()
	var setup struct {
		MCPServers []struct{ Name, Type, ServerID, Command string }
	}
	_ = json.Unmarshal(create.Params, &setup)
	if create.Method != "session/new" || len(setup.MCPServers) != 1 {
		os.Exit(4)
	}
	server := setup.MCPServers[0]
	if on {
		if server.Type != "acp" || server.ServerID == "" || server.Command != "" {
			os.Exit(5)
		}
		call := func(outer string, inner string, session string) {
			fields := map[string]any{"method": "mcp/message", "params": MCPACPRequest{
				ServerID: server.ServerID, RequestID: inner, SessionID: session, Method: "tools/call",
				Params: json.RawMessage(`{"literalNumber":9007199254740993}`),
			}}
			if outer != "" {
				fields["id"] = outer
			}
			send(fields)
		}
		notify := func(want string) {
			msg := receive()
			var request MCPACPRequest
			_ = json.Unmarshal(msg.Params, &request)
			if msg.Method != "mcp/message" || len(msg.ID) != 0 || request.RequestID != want || request.ServerID != server.ServerID ||
				request.Method != "notifications/progress" {
				os.Exit(6)
			}
		}
		checkError := func(id string, code int) {
			msg := receive()
			if string(msg.ID) != strconv.Quote(id) || msg.Error == nil || msg.Error.Code != code {
				os.Exit(7)
			}
		}
		call("", "ignore-this-notification", "")
		call("foreign", "foreign", "wrong-session")
		checkError("foreign", -32602)
		call("wire-1", "literal $HOME", "")
		notify("literal $HOME")
		got := receive()
		if string(got.ID) != `"wire-1"` || string(got.Result) != `{"result":null}` {
			os.Exit(8)
		}
		call("wire-2", "blocked", "")
		notify("blocked")
		call("duplicate", "blocked", "")
		checkError("duplicate", -32602)
		send(map[string]any{"method": "$/cancel_request", "params": map[string]string{"requestId": "wire-2"}})
		checkError("wire-2", -32800)
	} else if server.Command != "fixture-stdio" || server.ServerID != "" || init.ProtocolVersion == 2 && server.Type != "stdio" {
		os.Exit(9)
	}
	send(map[string]any{"id": create.ID, "result": map[string]string{"sessionId": "mcp-fixture"}})
	for scanner.Scan() {
	}
	os.Exit(0)
}
