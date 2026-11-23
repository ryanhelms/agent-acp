// Package nativehistory reads retained conversations through the harness owner's
// passive APIs. Historical records cannot issue new permission requests.
package nativehistory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	model "agent-acp.local/sdks/go/model"
)

// ForHarness returns a ReadHistory hook for a local Claude or Codex profile.
// Unknown providers return a nil hook so capabilities accurately describe them.
// cwd is the project directory used when looking up Claude's session.
func ForHarness(provider, cwd string) func(context.Context, *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
	var read func(context.Context, string, string, string) ([]*model.Observation, string, error)
	switch provider {
	case "claude":
		read = claudeBatch
	case "codex":
		read = codexBatch
	default:
		return nil
	}
	return func(ctx context.Context, req *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
		ref := req.GetSession()
		if ref.GetHarnessInstance() == "" || ref.GetSessionId() == "" {
			return nil, errors.New("history requires a harness instance and session ID")
		}
		scope := fmt.Sprintf("%x", sha256.Sum256([]byte(provider+"\x00"+ref.HarnessInstance+"\x00"+ref.SessionId)))
		position := pageCursor{Scope: scope}
		if value := req.GetCursor(); value != "" {
			position = pageCursor{}
			bytes, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil || json.Unmarshal(bytes, &position) != nil || position.Scope != scope || position.Skip < 0 {
				return nil, errors.New("invalid history cursor for this session")
			}
		}
		observations, next, err := read(ctx, cwd, ref.SessionId, position.Native)
		if err != nil {
			return nil, err
		}
		if position.Skip > len(observations) {
			return nil, errors.New("retained history changed since the last page")
		}
		limit := int(req.GetLimit())
		if limit == 0 {
			limit = 100
		}
		count := min(limit, len(observations)-position.Skip)
		response := &model.ReadHistoryResponse{Observations: observations[position.Skip : position.Skip+count]}
		for _, observation := range response.Observations {
			position.Skip++
			observation.Session = &model.SessionRef{HarnessInstance: ref.HarnessInstance, SessionId: ref.SessionId}
			observation.Cursor = position.encode()
		}
		if position.Skip < len(observations) {
			response.NextCursor = position.encode()
		} else if next != "" {
			response.NextCursor = pageCursor{Scope: scope, Native: next}.encode()
		}
		return response, nil
	}
}

// One native page may contain more than the requested number of observations,
// for example a Codex turn containing several tool calls.
type pageCursor struct {
	Scope  string `json:"s"`
	Native string `json:"n,omitempty"`
	Skip   int    `json:"i,omitempty"`
}

func (c pageCursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func text(speaker model.Speaker, value string) *model.Observation {
	return &model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: speaker, Text: value}}}
}

func tool(id, name, phase, summary string) *model.Observation {
	return &model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{CallId: id, Name: name, Phase: phase, Summary: summary}}}
}

func extension(name string, raw json.RawMessage) *model.Observation {
	return &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{Type: name, MediaType: "application/json", Data: raw}}}
}
