package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sync"

	model "agent-acp.local/sdks/go/model"
	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/protobuf/proto"
)

// ACPReplaySource identifies original updates alongside normalized history records.
const ACPReplaySource = "acp.replay_update"

const (
	maxReplayRecord = 720 << 10
	maxReplayPage   = 4 << 20
	maxReplayLimit  = 128
)

type replayCursor struct {
	Scope string
	Index int
	Hash  string
}

func (cursor replayCursor) encode() string {
	body, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}

type replayPage struct {
	mu         sync.Mutex
	collecting bool
	session    *model.SessionRef
	wanted     replayCursor
	position   replayCursor
	prefix     hash.Hash
	matched    bool
	limit      int
	size       int
	response   model.ReadHistoryResponse
	next       string
	err        error
}

func (r *Runtime) replayHistory(ctx context.Context, request *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
	id := request.Session.SessionId
	r.mu.Lock()
	active := r.sessions[id]
	if (active != nil && !active.p.disconnected()) || r.resuming[id] != nil {
		r.mu.Unlock()
		return nil, errors.New("ACP replay requires an inactive session")
	}
	ready := make(chan struct{})
	r.resuming[id] = ready
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.resuming, id)
		close(ready)
		r.mu.Unlock()
	}()

	scope := sha256.Sum256([]byte(r.config.HarnessInstance + "\x00" + request.Session.SessionId + "\x00" + r.config.WorkingDirectory))
	var cursor replayCursor
	cursor.Scope = hex.EncodeToString(scope[:])
	wanted := cursor
	if request.Cursor != "" {
		if len(request.Cursor) > 2048 {
			return nil, errors.New("invalid ACP replay cursor")
		}
		body, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(body, &wanted) != nil || wanted.Scope != cursor.Scope ||
			wanted.Index < 1 || len(wanted.Hash) != sha256.Size*2 {
			return nil, errors.New("invalid ACP replay cursor for this session")
		}
	}

	limit := int(min(request.Limit, maxReplayLimit))
	if limit == 0 {
		limit = 32
	}
	page := &replayPage{session: request.Session, wanted: wanted, position: cursor,
		prefix: sha256.New(), matched: wanted.Index == 0, limit: limit}
	peer, init, err := r.connect(ctx, page.handle)
	if err != nil {
		return nil, err
	}
	defer peer.close()
	if !init.AgentCapabilities.LoadSession {
		return nil, errors.New("adapter does not advertise ACP session loading")
	}
	page.mu.Lock()
	page.collecting = true
	page.mu.Unlock()
	if _, err := rpc[empty](ctx, peer, "session/load", map[string]any{
		"sessionId": request.Session.SessionId, "cwd": r.config.WorkingDirectory, "mcpServers": []empty{},
	}); err != nil {
		return nil, err
	}
	page.mu.Lock()
	defer page.mu.Unlock()
	page.collecting = false
	if page.err != nil {
		return nil, page.err
	}
	if !page.matched {
		return nil, errors.New("ACP replay changed since the previous page")
	}
	page.response.NextCursor = page.next
	return &page.response, nil
}

func (page *replayPage) handle(_ context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "session/request_permission":
		// A historical request never becomes a live approval.
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	case "session/update":
		var message struct {
			SessionID string
			Update    json.RawMessage
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, acp.NewInvalidParams(err.Error())
		}
		page.mu.Lock()
		defer page.mu.Unlock()
		if !page.collecting || message.SessionID != page.session.SessionId || page.next != "" || page.err != nil {
			return nil, nil
		}
		for _, record := range replayRecords(message.Update) {
			page.add(record)
			if page.next != "" || page.err != nil {
				break
			}
		}
		return nil, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func replayRecords(raw json.RawMessage) []*model.Observation {
	if len(raw) > maxReplayRecord {
		return []*model.Observation{{Event: &model.Observation_Gap{Gap: &model.HistoryGap{
			Reason: "An ACP replay record exceeded the history size limit",
		}}}}
	}
	// Canonicalize JSON object order so equivalent provider encodings give the
	// same cursor prefix. Keep all supplied fields and the original JSON types.
	var body any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err == nil {
		if canonical, err := json.Marshal(body); err == nil {
			raw = canonical
		}
	}
	original := &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{
		Type: ACPReplaySource, MediaType: "application/json", Data: raw,
	}}}
	record := normalizeUpdate(raw)
	if record == nil {
		return []*model.Observation{original}
	}
	if record.GetExtension() != nil {
		return []*model.Observation{record}
	}
	records := []*model.Observation{record, original}
	if tool := record.GetTool(); tool != nil {
		var update struct{ SessionUpdate string }
		_ = json.Unmarshal(raw, &update)
		if update.SessionUpdate == "tool_call" && terminalToolPhase(tool.Phase) {
			// Some providers replay a completed call in one update. Preserve
			// its name and identity as well as the result.
			call := &model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{
				CallId: tool.CallId, Name: tool.Name, Phase: "tool_call",
			}}}
			records = append([]*model.Observation{call}, records...)
		}
	}
	return records
}

func terminalToolPhase(phase string) bool {
	switch phase {
	case "completed", "failed", "declined", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func (page *replayPage) add(record *model.Observation) {
	size := proto.Size(record)
	if page.matched && (len(page.response.Observations) >= page.limit || page.size+size > maxReplayPage) {
		page.next = page.position.encode()
		return
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(record)
	if err != nil {
		page.err = fmt.Errorf("encode ACP replay record: %w", err)
		return
	}
	var width [8]byte
	binary.BigEndian.PutUint64(width[:], uint64(len(encoded)))
	_, _ = page.prefix.Write(width[:])
	_, _ = page.prefix.Write(encoded)
	page.position.Index++
	page.position.Hash = hex.EncodeToString(page.prefix.Sum(nil))
	if !page.matched {
		if page.position.Index == page.wanted.Index {
			if page.position.Hash != page.wanted.Hash {
				page.err = errors.New("ACP replay changed since the previous page")
			} else {
				page.matched = true
			}
		}
		return
	}
	record.Session, record.Cursor = page.session, page.position.encode()
	page.size += size
	page.response.Observations = append(page.response.Observations, record)
}
