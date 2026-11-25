package runtime

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
)

const (
	maxElicitationPayload = 32 << 10
	maxElicitationCount   = 16
)

// Elicitation implements a local host's user interaction, separately from
// tool permission approvals. Only the supplied modes are advertised.
type Elicitation struct {
	Form, URL bool
	Create    func(context.Context, ElicitationRequest) (ElicitationResponse, error)
	Complete  func(context.Context, ElicitationRequest)
}

type ElicitationRequest struct {
	SessionID       string          `json:"sessionId"`
	RequestID       json.RawMessage `json:"requestId,omitempty"`
	ToolCallID      string          `json:"toolCallId,omitempty"`
	Mode            string          `json:"mode"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	ElicitationID   string          `json:"elicitationId,omitempty"`
	URL             string          `json:"url,omitempty"`
}

type ElicitationResponse struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

type elicitationState struct {
	cfg     *Elicitation
	mu      sync.Mutex
	pending int
	urls    map[string]elicitationURL
}

type elicitationURL struct {
	request ElicitationRequest
	expires time.Time
}

func (e *Elicitation) capabilities() map[string]any {
	if e == nil || e.Create == nil {
		return nil
	}
	modes := make(map[string]any, 2)
	if e.Form {
		modes["form"] = struct{}{}
	}
	if e.URL {
		modes["url"] = struct{}{}
	}
	if len(modes) == 0 {
		return nil
	}
	return map[string]any{"elicitation": modes}
}

// HTTPS destinations are surfaced for explicit consent. The runtime does not
// resolve or fetch them, and rejects embedded credentials and escaped controls.
func ElicitationURLHost(raw string) (string, bool) {
	if len(raw) > 4096 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return "", false
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.User != nil {
		return "", false
	}
	return u.Host, true
}

func (s *session) createElicitation(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	state := &s.elicitation
	cfg := state.cfg
	if cfg == nil || cfg.Create == nil || !transport.IsInboundRequest(ctx) {
		return nil, acp.NewMethodNotFound("elicitation/create")
	}
	var req ElicitationRequest
	if len(raw) > maxElicitationPayload || json.Unmarshal(raw, &req) != nil ||
		strings.TrimSpace(req.Message) == "" || len(req.Message) > 4096 ||
		len(req.ToolCallID) > 256 {
		return nil, acp.NewInvalidParams("invalid ACP elicitation")
	}
	active := s.wireSession.Load()
	if req.SessionID == "" || len(req.RequestID) != 0 || active == nil || *active != req.SessionID {
		return nil, acp.NewInvalidParams("elicitation must address this active session")
	}
	switch req.Mode {
	case "form":
		if !cfg.Form || !jsonObject(req.RequestedSchema) || req.URL != "" || req.ElicitationID != "" {
			return nil, acp.NewInvalidParams("invalid or unsupported form elicitation")
		}
	case "url":
		_, validURL := ElicitationURLHost(req.URL)
		if !cfg.URL || !validURL || req.ElicitationID == "" || len(req.ElicitationID) > 256 || len(req.RequestedSchema) != 0 {
			return nil, acp.NewInvalidParams("invalid or unsupported URL elicitation")
		}
	default:
		return nil, acp.NewInvalidParams("unknown elicitation mode")
	}
	state.mu.Lock()
	for id, tracked := range state.urls {
		if !tracked.expires.IsZero() && time.Now().After(tracked.expires) {
			delete(state.urls, id)
		}
	}
	_, duplicate := state.urls[req.ElicitationID]
	if state.pending+len(state.urls) >= maxElicitationCount || duplicate {
		state.mu.Unlock()
		return nil, acp.NewInvalidParams("elicitation limit exceeded or ID already active")
	}
	if req.Mode == "url" {
		if state.urls == nil {
			state.urls = make(map[string]elicitationURL)
		}
		state.urls[req.ElicitationID] = elicitationURL{request: req}
	} else {
		state.pending++
	}
	state.mu.Unlock()
	acceptedURL := false
	defer func() {
		state.mu.Lock()
		defer state.mu.Unlock()
		if req.Mode == "form" {
			state.pending--
		} else if !acceptedURL {
			delete(state.urls, req.ElicitationID)
		} else {
			state.urls[req.ElicitationID] = elicitationURL{request: req, expires: time.Now().Add(15 * time.Minute)}
		}
	}()
	response, err := cfg.Create(ctx, req)
	if ctx.Err() != nil {
		return ElicitationResponse{Action: "cancel"}, nil
	}
	if err != nil {
		return nil, acp.NewInvalidParams("the client could not present this elicitation")
	}
	switch response.Action {
	case "decline", "cancel":
		response.Content = nil
	case "accept":
		if req.Mode == "url" {
			acceptedURL = true
			response.Content = nil
		} else if len(response.Content) > maxElicitationPayload ||
			len(response.Content) > 0 && !jsonObject(response.Content) {
			return nil, acp.NewInvalidParams("invalid form reply")
		}
	default:
		return nil, acp.NewInvalidParams("invalid elicitation action")
	}
	return response, nil
}

func (s *session) completeElicitation(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	state := &s.elicitation
	cfg := state.cfg
	if cfg == nil || !cfg.URL || transport.IsInboundRequest(ctx) {
		return nil, acp.NewMethodNotFound("elicitation/complete")
	}
	var request struct {
		ElicitationID string `json:"elicitationId"`
	}
	if len(raw) > 1024 || json.Unmarshal(raw, &request) != nil {
		return nil, nil
	}
	state.mu.Lock()
	tracked, exists := state.urls[request.ElicitationID]
	notify := exists && !tracked.expires.IsZero() && !time.Now().After(tracked.expires)
	if notify {
		delete(state.urls, request.ElicitationID)
	}
	state.mu.Unlock()
	if notify && cfg.Complete != nil {
		cfg.Complete(ctx, tracked.request)
	}
	return nil, nil
}
