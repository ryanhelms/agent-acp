// Package nativehistory reads retained conversations through the harness owner's
// passive APIs. Historical records cannot issue new permission requests.
package nativehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
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
		position := pageCursor{Scope: scope, Version: 1}
		if value := req.GetCursor(); value != "" {
			position = pageCursor{}
			bytes, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil || json.Unmarshal(bytes, &position) != nil || position.Scope != scope || position.Skip < 0 || position.Version < 0 || position.Version > 1 {
				return nil, errors.New("invalid history cursor for this session")
			}
		}
		observations, next, err := read(ctx, cwd, ref.SessionId, position.Native)
		if err != nil {
			return nil, err
		}
		if err := position.seek(observations); err != nil {
			return nil, err
		}

		limit := int(req.GetLimit())
		if limit == 0 {
			limit = 100
		}
		response := &model.ReadHistoryResponse{}
		start := position.Skip
		end := start + min(limit, len(observations)-start)
		sourceEnd, owner, byteCount := start, start, 0
		var origin string
		for i := start; i < end; i++ {
			observation := observations[i]
			if i == sourceEnd {
				owner, sourceEnd = sourceGroup(observations, i)
				sourcePosition := position
				sourcePosition.Skip = owner + 1
				origin = sourcePosition.encode()
			}
			observation.SourceCursor = proto.String(origin)
			if i != owner || observation.GetExtension() == observation.SourceRecord {
				observation.SourceRecord = nil
			}
			observation.Session = ref
			position.Skip = i + 1
			if i == owner {
				observation.Cursor = origin
			} else {
				observation.Cursor = position.encode()
			}
			size := proto.Size(observation)
			// Return an individually large record intact, in a page of its own.
			if i > start && byteCount+size > 1<<20 {
				position.Skip = i
				break
			}
			byteCount += size
			response.Observations = append(response.Observations, observation)
		}
		if position.Skip < len(observations) {
			response.NextCursor = position.encode()
		} else if next != "" {
			response.NextCursor = pageCursor{Scope: scope, Native: next, Version: 1}.encode()
		}
		return response, nil
	}
}

// One native page may contain more than the requested number of observations,
// for example a Codex turn containing several tool calls.
type pageCursor struct {
	Version int    `json:"v,omitempty"`
	Scope   string `json:"s"`
	Native  string `json:"n,omitempty"`
	Skip    int    `json:"i,omitempty"`
}

func (position *pageCursor) seek(observations []*model.Observation) error {
	if position.Version == 0 {
		// Older pages had no stand-alone entry for an unfinished turn or
		// empty Codex user item. Keep their saved next-page cursors usable.
		oldSkip := position.Skip
		position.Skip, position.Version = 0, 1
		for oldSkip > 0 {
			if position.Skip == len(observations) {
				return errors.New("retained history changed since the last page")
			}
			kind := observations[position.Skip].GetExtension().GetType()
			if kind != "codex.history.turn" && kind != "codex.history.emptyItem" {
				oldSkip--
			}
			position.Skip++
		}
	}
	if position.Skip > len(observations) {
		return errors.New("retained history changed since the last page")
	}
	return nil
}

func sourceGroup(observations []*model.Observation, i int) (owner, end int) {
	source := observations[i].SourceRecord
	owner = i
	for owner > 0 && observations[owner-1].SourceRecord == source {
		owner--
	}
	end = i + 1
	for end < len(observations) && observations[end].SourceRecord == source {
		end++
	}
	// An existing opaque event can own the original, even when the
	// first normalized event belongs to another page.
	for j := owner; j < end; j++ {
		if observations[j].GetExtension() == source {
			owner = j
			break
		}
	}
	return owner, end
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

// All normalized observations temporarily share one pointer to their original
// within the native batch. ForHarness emits the bytes at the owning cursor once.
func withSource(kind string, raw json.RawMessage, events []*model.Observation) []*model.Observation {
	source := &model.Extension{Type: kind, MediaType: "application/json", Data: raw}
	for _, event := range events {
		if already := event.GetExtension(); already != nil && bytes.Equal(already.Data, raw) {
			source = already
			break
		}
	}
	for _, event := range events {
		event.SourceRecord = source
	}
	return events
}
