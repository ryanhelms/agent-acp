// Package nativehistory reads retained conversations through the harness owner's
// passive APIs. Historical records cannot issue new permission requests.
package nativehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

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
		position := pageCursor{Scope: scope, Version: 2}
		if value := req.GetCursor(); value != "" {
			position = pageCursor{}
			bytes, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil || json.Unmarshal(bytes, &position) != nil || position.Scope != scope || position.Skip < 0 || !position.validVersion() {
				return nil, errors.New("invalid history cursor for this session")
			}
		}
		limit := int(req.GetLimit())
		if limit == 0 {
			limit = 100
		}
		var visited map[string]bool
		for {
			observations, next, err := read(ctx, cwd, ref.SessionId, position.Native)
			if err != nil {
				return nil, err
			}
			prefix := batchPrefix{hash: sha256.New()}
			if err := position.seek(observations); err != nil {
				return changedHistory(ref), nil
			}
			if position.Skip > 0 {
				prefix.through(observations, position.Skip-1)
				if position.Version == 2 && position.Prefix != prefix.digest {
					return changedHistory(ref), nil
				}
			}
			if position.Skip < len(observations) {
				return historyPage(ref, position, observations, &prefix, limit, next != ""), nil
			}
			if next == "" {
				return &model.ReadHistoryResponse{}, nil
			}
			if visited == nil {
				visited = make(map[string]bool)
			}
			visited[position.Native] = true
			if visited[next] {
				return nil, errors.New("native history cursor did not advance")
			}
			position = pageCursor{Scope: scope, Native: next, Version: 2}
		}
	}
}

func changedHistory(ref *model.SessionRef) *model.ReadHistoryResponse {
	gap := &model.HistoryGap{Reason: "Retained history changed since the last page. Reload the session history to inspect its current records."}
	return &model.ReadHistoryResponse{Observations: []*model.Observation{{Session: ref, Event: &model.Observation_Gap{Gap: gap}}}}
}

func historyPage(ref *model.SessionRef, position pageCursor, observations []*model.Observation, prefix *batchPrefix, limit int, hasNext bool) *model.ReadHistoryResponse {
	response := &model.ReadHistoryResponse{}
	start := position.Skip
	end := start + min(limit, len(observations)-start)
	byteCount := 0
	sourceCursor := ""
	for i := start; i < end; i++ {
		if sourceCursor == "" || i == prefix.end {
			prefix.through(observations, i)
			position.Version, position.Prefix = 2, prefix.digest
			position.Skip = prefix.owner + 1
			sourceCursor = position.encode()
		}
		observation := observations[i]
		observation.SourceCursor = proto.String(sourceCursor)
		if i != prefix.owner || observation.GetExtension() == observation.SourceRecord {
			observation.SourceRecord = nil
		}
		observation.Session = ref
		observation.Cursor = sourceCursor
		if i != prefix.owner {
			position.Skip = i + 1
			observation.Cursor = position.encode()
		}
		size := proto.Size(observation)
		// Return an individually large record intact, in a page of its own.
		if i > start && byteCount+size > 1<<20 {
			break
		}
		byteCount += size
		response.Observations = append(response.Observations, observation)
	}
	if start+len(response.Observations) < len(observations) || hasNext {
		// The last delivered row also anchors the transition to the next native page.
		response.NextCursor = response.Observations[len(response.Observations)-1].Cursor
	}
	return response
}

// Hash each original once per native batch read, even if it supplied many rows.
// Framed values distinguish field boundaries without re-encoding the provider JSON.
type batchPrefix struct {
	hash       hash.Hash
	end, owner int
	digest     string
}

func (prefix *batchPrefix) through(observations []*model.Observation, index int) {
	if index < prefix.end {
		return
	}
	var frame [8]byte
	length := func(n int) {
		binary.BigEndian.PutUint64(frame[:], uint64(n))
		prefix.hash.Write(frame[:])
	}
	field := func(data []byte) {
		length(len(data))
		prefix.hash.Write(data)
	}
	for prefix.end <= index {
		first := prefix.end
		source := observations[first].GetSourceRecord()
		prefix.owner, prefix.end = sourceGroup(observations, first)
		length(prefix.end - first)
		field([]byte(observations[first].GetTurnId()))
		field([]byte(source.GetType()))
		field(source.GetData())
	}
	var sum [sha256.Size]byte
	prefix.digest = base64.RawURLEncoding.EncodeToString(prefix.hash.Sum(sum[:0]))
}

// One native page may contain more than the requested number of observations,
// for example a Codex turn containing several tool calls.
type pageCursor struct {
	Version int    `json:"v,omitempty"`
	Scope   string `json:"s"`
	Native  string `json:"n,omitempty"`
	Skip    int    `json:"i,omitempty"`
	Prefix  string `json:"h,omitempty"`
}

func (position pageCursor) validVersion() bool {
	switch position.Version {
	case 0, 1:
		return position.Prefix == ""
	case 2:
		if position.Skip <= 0 || len(position.Prefix) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
			return false
		}
		digest, err := base64.RawURLEncoding.Strict().DecodeString(position.Prefix)
		return err == nil && len(digest) == sha256.Size
	}
	return false
}

func (position *pageCursor) seek(observations []*model.Observation) error {
	if position.Version == 0 {
		// Only count rows present in the v0 Codex view.
		oldSkip := position.Skip
		position.Skip, position.Version = 0, 1
		for oldSkip > 0 {
			if position.Skip == len(observations) {
				return errors.New("retained history changed since the last page")
			}
			kind := observations[position.Skip].GetExtension().GetType()
			if kind != "codex.history.turn" && kind != "codex.history.emptyItem" && kind != "codex.history.thread" {
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

func sourceGroup(observations []*model.Observation, first int) (owner, end int) {
	source := observations[first].SourceRecord
	owner, end = first, first+1
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
