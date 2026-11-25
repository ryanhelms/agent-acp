package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
)

// ACPMCPProtocolVersion is the sole MCP revision of the stateless ACP draft
// pinned at agentclientprotocol/agent-client-protocol d1585b5932ec.
const ACPMCPProtocolVersion = "2026-07-28"

const (
	acpMCPMethod   = "mcp/message"
	mcpPayloadMax  = 1 << 20
	mcpConcurrency = 16
)

// MCPACPRequest carries one independently attributed call. Params contains the
// original inner MCP params, including its per-request client metadata.
type MCPACPRequest struct {
	ServerID   string                                               `json:"serverId"`
	RequestID  string                                               `json:"requestId"`
	SessionID  string                                               `json:"sessionId,omitempty"`
	ToolCallID string                                               `json:"toolCallId,omitempty"`
	Method     string                                               `json:"method"`
	Params     json.RawMessage                                      `json:"params,omitempty"`
	Notify     func(context.Context, string, json.RawMessage) error `json:"-"`
}

// MCPACPResponse preserves an MCP result or error. A Go error from the
// callback instead means no attributable MCP response could be obtained.
type MCPACPResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type acpMCPRegistration struct {
	call   func(context.Context, MCPACPRequest) (MCPACPResponse, error)
	mu     sync.Mutex
	active map[string]struct{}
}

type acpMCPBindings struct {
	mu      sync.Mutex
	conn    *transport.Connection
	servers map[string]*acpMCPRegistration
}

func jsonObject(value []byte) bool {
	value = bytes.TrimSpace(value)
	return len(value) != 0 && value[0] == '{' && json.Valid(value)
}

func (s *session) handleMCPACP(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	// An outer notification has no response channel and cannot invoke tools.
	if !transport.IsInboundRequest(ctx) {
		return nil, acp.NewMethodNotFound(acpMCPMethod)
	}
	if len(raw) > mcpPayloadMax {
		return nil, acp.NewRequestError(-33000, "MCP request exceeds the binding limit", nil)
	}
	var request MCPACPRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.ServerID == "" ||
		request.RequestID == "" || len(request.RequestID) > 256 || request.Method == "" || len(request.Method) > 256 ||
		(request.ToolCallID != "" && request.SessionID == "") ||
		(len(request.Params) != 0 && !bytes.Equal(request.Params, []byte("null")) && !jsonObject(request.Params)) {
		return nil, acp.NewInvalidParams("invalid MCP-over-ACP request envelope")
	}
	s.mcp.mu.Lock()
	registration := s.mcp.servers[request.ServerID]
	conn := s.mcp.conn
	s.mcp.mu.Unlock()
	if registration == nil {
		return nil, acp.NewRequestError(-33001, "MCP registration unavailable", nil)
	}
	if request.SessionID != "" {
		active := s.wireSession.Load()
		if active == nil || *active != request.SessionID {
			return nil, acp.NewInvalidParams("MCP request does not address the registered session")
		}
	}
	registration.mu.Lock()
	if _, duplicate := registration.active[request.RequestID]; duplicate {
		registration.mu.Unlock()
		return nil, acp.NewInvalidParams("MCP request ID is already active")
	}
	if len(registration.active) >= mcpConcurrency {
		registration.mu.Unlock()
		return nil, acp.NewRequestError(-33000, "MCP registration is at capacity", nil)
	}
	registration.active[request.RequestID] = struct{}{}
	registration.mu.Unlock()
	defer func() {
		registration.mu.Lock()
		delete(registration.active, request.RequestID)
		registration.mu.Unlock()
	}()

	if ctx.Err() != nil {
		return nil, acp.NewRequestCancelled(nil)
	}
	var notificationMu sync.Mutex
	finished := false
	request.Notify = func(sendCtx context.Context, method string, params json.RawMessage) error {
		notificationMu.Lock()
		defer notificationMu.Unlock()
		if finished || ctx.Err() != nil || sendCtx.Err() != nil {
			return context.Canceled
		}
		if !strings.HasPrefix(method, "notifications/") || len(params) > mcpPayloadMax ||
			(len(params) != 0 && !bytes.Equal(params, []byte("null")) && !jsonObject(params)) {
			return errors.New("invalid request-scoped MCP notification")
		}
		return conn.SendNotification(ctx, acpMCPMethod, MCPACPRequest{
			ServerID: request.ServerID, RequestID: request.RequestID, Method: method, Params: params,
		})
	}
	response, err := registration.call(ctx, request)
	notificationMu.Lock()
	finished = true
	notificationMu.Unlock()
	if ctx.Err() != nil {
		return nil, acp.NewRequestCancelled(nil)
	}
	if err != nil || len(response.Result)+len(response.Error) > mcpPayloadMax ||
		(len(response.Result) == 0) == (len(response.Error) == 0) ||
		(len(response.Result) != 0 && !json.Valid(response.Result)) ||
		(len(response.Error) != 0 && !jsonObject(response.Error)) {
		return nil, acp.NewRequestError(-33002, "MCP backend produced no valid outcome", nil)
	}
	return response, nil
}
