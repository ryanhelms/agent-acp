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
	"agent-acp.local/transport"
	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/protobuf/proto"
)

// ACPReplaySource identifies original updates alongside normalized history records.
const ACPReplaySource = "acp.replay_update"

// ErrReplaySnapshotLimit reports that a complete passive replay did not fit
// in the snapshot's retention budget.
var ErrReplaySnapshotLimit = errors.New("ACP replay exceeds the snapshot retention limit")

const (
	maxReplayRecord          = 720 << 10
	maxReplayPage            = 4 << 20
	maxReplayLimit           = 128
	maxReplaySnapshotBytes   = 32 << 20
	maxReplaySnapshotRecords = 32 << 10
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
	mu          sync.Mutex
	collecting  bool
	session     *model.SessionRef
	wanted      *replayCursor
	snapshot    bool
	position    replayCursor
	prefix      hash.Hash
	matched     bool
	limit       int
	size        int
	response    model.ReadHistoryResponse
	next        string
	err         error
	v2          bool
	replayDone  bool
	stateSeen   bool
	children    childSessions
	childReplay map[string]bool
	compactions *compactionState
}

// ReplaySnapshot is a bounded, immutable view of one successful passive load.
// Its cursors still identify provider content, so a rebuilt unchanged snapshot
// can resolve existing links.
type ReplaySnapshot struct {
	scope string
	rows  []*model.Observation
	bytes int
}

func (s *ReplaySnapshot) Size() int { return s.bytes }

// Page returns an isolated copy of the next bounded page.
func (s *ReplaySnapshot) Page(cursor string, limit uint32) (*model.ReadHistoryResponse, error) {
	after, err := s.index(cursor)
	if err != nil {
		return nil, err
	}
	out := &model.ReadHistoryResponse{}
	size, count := 0, replayLimit(limit)
	for _, row := range s.rows[after:] {
		width := proto.Size(row)
		if len(out.Observations) >= count || size+width > maxReplayPage {
			out.NextCursor = out.Observations[len(out.Observations)-1].Cursor
			break
		}
		out.Observations = append(out.Observations, proto.CloneOf(row))
		size += width
	}
	return out, nil
}

// Record resolves one exact original without scanning a page of neighboring rows.
func (s *ReplaySnapshot) Record(cursor string) (*model.Observation, error) {
	index, err := s.index(cursor)
	if err != nil {
		return nil, err
	}
	if index == 0 {
		return nil, errors.New("ACP replay record requires a cursor")
	}
	return proto.CloneOf(s.rows[index-1]), nil
}

func (s *ReplaySnapshot) index(cursor string) (int, error) {
	wanted, err := parseReplayCursor(cursor, s.scope)
	if err != nil || wanted.Index == 0 {
		return 0, err
	}
	if wanted.Index > len(s.rows) || s.rows[wanted.Index-1].Cursor != wanted.encode() {
		return 0, errors.New("ACP replay changed since the previous page")
	}
	return wanted.Index, nil
}

func (r *Runtime) replayScope(ref *model.SessionRef) string {
	scope := sha256.Sum256([]byte(r.config.HarnessInstance + "\x00" + ref.SessionId + "\x00" + r.config.WorkingDirectory))
	return hex.EncodeToString(scope[:])
}

func parseReplayCursor(cursor, scope string) (replayCursor, error) {
	wanted := replayCursor{Scope: scope}
	if cursor == "" {
		return wanted, nil
	}
	if len(cursor) > 2048 {
		return wanted, errors.New("invalid ACP replay cursor")
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(body, &wanted) != nil || wanted.Scope != scope ||
		wanted.Index < 1 || len(wanted.Hash) != sha256.Size*2 {
		return wanted, errors.New("invalid ACP replay cursor for this session")
	}
	return wanted, nil
}

func replayLimit(limit uint32) int {
	if limit == 0 {
		return 32
	}
	return int(min(limit, maxReplayLimit))
}

// LoadReplay loads an inactive session once with no MCP servers and creates a
// snapshot only if the complete replay fits inside the finite retention budget.
func (r *Runtime) LoadReplay(ctx context.Context, ref *model.SessionRef) (*ReplaySnapshot, error) {
	if err := r.validate(ref); err != nil {
		return nil, err
	}
	if !r.config.ReplayHistory {
		return nil, errors.New("ACP replay is unavailable for this harness")
	}
	scope := r.replayScope(ref)
	page := &replayPage{
		session: proto.CloneOf(ref), position: replayCursor{Scope: scope},
		snapshot: true, matched: true, prefix: sha256.New(),
	}
	if err := r.loadReplay(ctx, page); err != nil {
		return nil, err
	}
	return &ReplaySnapshot{scope: scope, rows: page.response.Observations, bytes: page.size}, nil
}

func (r *Runtime) replayHistory(ctx context.Context, request *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
	scope := r.replayScope(request.Session)
	wanted, err := parseReplayCursor(request.Cursor, scope)
	if err != nil {
		return nil, err
	}
	page := &replayPage{
		session: request.Session, wanted: &wanted, position: replayCursor{Scope: scope},
		prefix: sha256.New(), matched: wanted.Index == 0, limit: replayLimit(request.Limit),
	}
	if err := r.loadReplay(ctx, page); err != nil {
		return nil, err
	}
	page.response.NextCursor = page.next
	return &page.response, nil
}

func (r *Runtime) loadReplay(ctx context.Context, page *replayPage) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	id := page.session.SessionId
	if r.knownChild(id) {
		return errors.New("replay a child's history through its parent session")
	}
	r.mu.Lock()
	active := r.sessions[id]
	if (active != nil && !active.p.disconnected()) || r.resuming[id] != nil {
		r.mu.Unlock()
		return errors.New("ACP replay requires an inactive session")
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

	peer, init, err := r.connectWithOptions(ctx, page.handle, page.selectWire, transport.Options{
		HandleNotificationsInline: true,
	})
	if err != nil {
		return err
	}
	defer peer.close()
	method := "session/load"
	params := map[string]any{"sessionId": id, "cwd": r.config.WorkingDirectory, "mcpServers": []empty{}}
	if peer.version == 2 {
		if init.Capabilities.Session == nil {
			return errors.New("adapter does not advertise ACP session support")
		}
		method = "session/resume"
		params["replayFrom"] = map[string]string{"type": "start"}
	} else if !init.AgentCapabilities.LoadSession {
		return errors.New("adapter does not advertise ACP session loading")
	}
	page.mu.Lock()
	page.v2 = peer.version == 2
	if page.v2 || r.config.ChildSessions {
		page.children = make(childSessions)
		page.childReplay = make(map[string]bool)
	}
	if page.v2 || r.config.CompactionUpdates {
		page.compactions = &compactionState{v2: page.v2}
	}
	page.collecting = true
	page.mu.Unlock()
	if _, err := rpc[empty](ctx, peer, method, params); err != nil {
		return err
	}
	page.mu.Lock()
	defer page.mu.Unlock()
	page.collecting = false
	if page.err != nil {
		return page.err
	}
	if page.v2 && (!page.replayDone || !page.stateSeen) {
		return errors.New("ACP v2 replay omitted its replay marker or current session state")
	}
	if page.v2 {
		for child := range page.children {
			if !page.childReplay[child] {
				return errors.New("ACP v2 replay omitted a child replay marker")
			}
		}
	}
	if !page.matched {
		return errors.New("ACP replay changed since the previous page")
	}
	return nil
}

func (page *replayPage) selectWire(method, id string) bool {
	page.mu.Lock()
	defer page.mu.Unlock()
	_, child := page.children[id]
	return method == "session/update" && page.collecting && (page.session.SessionId == id || child) && page.next == "" && page.err == nil
}

func (page *replayPage) handle(ctx context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "session/request_permission":
		// A historical request never becomes a live approval.
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	case "session/update":
		message, err := decodeUpdateParams(raw)
		if err != nil {
			return nil, acp.NewInvalidParams(err.Error())
		}
		page.mu.Lock()
		defer page.mu.Unlock()
		root := page.session.SessionId
		_, child := page.children[message.id]
		if !page.collecting || message.id == "" || message.id != root && !child || page.err != nil {
			return nil, nil
		}
		if page.v2 && page.v2ReplayBoundary(message.id, message.update) {
			return nil, nil
		}
		if page.children != nil && childAnnouncement(message.update) && !page.children.announce(root, message.id, message.update) {
			return nil, nil
		}
		if page.next != "" || noticeUpdate(message.update) {
			return nil, nil
		}
		var records []*model.Observation
		wire, _ := wireRecord(transport.InboundReceipt(ctx, method, message.id), method, maxReplayRecord)
		var compaction *model.Observation
		if page.compactions != nil && !child {
			compaction = page.compactions.observe(message.update, uint64(page.position.Index)+1)
		}
		switch {
		case child:
			record, source := withSource(nil, ACPChildUpdateSource, raw)
			record.WireProvenance = wire
			boundEvidence(record, source, "replay_record_limit", maxReplayRecord)
			records = []*model.Observation{record}
		case compaction != nil:
			record, source := withSource(compaction, ACPUpdateParamsSource, raw)
			record.WireProvenance = wire
			boundEvidence(record, source, "replay_record_limit", maxReplayRecord)
			records = []*model.Observation{record}
		case message.extra || wire != nil:
			records = sourcedReplayRecords(message.update, raw, wire)
		default:
			records = replayRecords(message.update)
		}
		for _, record := range records {
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

func (page *replayPage) v2ReplayBoundary(id string, raw json.RawMessage) bool {
	var update struct{ SessionUpdate, State string }
	if json.Unmarshal(raw, &update) != nil {
		return false
	}
	if id != page.session.SessionId {
		if update.SessionUpdate == "replay_complete" {
			if page.childReplay[id] {
				page.err = errors.New("ACP v2 replay sent duplicate child completion markers")
			}
			page.childReplay[id] = true
			return true
		}
		return page.childReplay[id] || update.SessionUpdate == "state_update"
	}
	switch update.SessionUpdate {
	case "replay_complete":
		if page.replayDone {
			page.err = errors.New("ACP v2 replay sent duplicate completion markers")
		}
		page.replayDone = true
		return true
	case "state_update":
		if page.replayDone {
			switch update.State {
			case "idle", "running", "requires_action", "unknown":
				page.stateSeen = true
			}
		}
		return true
	default:
		return page.replayDone
	}
}

func sourcedReplayRecords(update, raw json.RawMessage, wire *model.WireProvenance) []*model.Observation {
	var semantic *model.Observation
	if len(update) <= maxReplayRecord {
		semantic = normalizeUpdate(update)
		if semantic.GetExtension() != nil {
			semantic = nil
		}
	}
	record, source := withSource(semantic, ACPUpdateParamsSource, raw)
	record.WireProvenance = wire
	boundEvidence(record, source, "replay_record_limit", maxReplayRecord)
	return prependCompletedCall([]*model.Observation{record}, update, semantic)
}

func replayRecords(raw json.RawMessage) []*model.Observation {
	if len(raw) > maxReplayRecord {
		return []*model.Observation{{Event: &model.Observation_Gap{Gap: &model.HistoryGap{
			Reason: "An ACP replay record exceeded the history size limit",
		}}}}
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
	return prependCompletedCall([]*model.Observation{record, original}, raw, record)
}

func canonicalReplayJSON(raw json.RawMessage) json.RawMessage {
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
	return raw
}

func prependCompletedCall(records []*model.Observation, raw json.RawMessage, record *model.Observation) []*model.Observation {
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
	if !page.snapshot && page.matched && (len(page.response.Observations) >= page.limit || page.size+size > maxReplayPage) {
		page.next = page.position.encode()
		return
	}
	// Store the exact original. Fingerprint its canonical field order so the
	// same provider values survive a differently ordered replay on the next page.
	source := record.GetSourceRecord()
	if source == nil {
		source = record.GetExtension()
	}
	sourced := source.GetType() == ACPUpdateParamsSource || source.GetType() == "acp.source_omitted"
	var original []byte
	if kind := source.GetType(); kind == ACPUpdateParamsSource || (source != record.GetSourceRecord() && source.GetMediaType() == "application/json") {
		original = source.Data
		source.Data = canonicalReplayJSON(original)
	}
	wire := record.WireProvenance
	extras := wire.GetAdditionalFieldsJson()
	if extras != "" {
		wire.AdditionalFieldsJson = string(canonicalReplayJSON([]byte(extras)))
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(record)
	if extras != "" {
		wire.AdditionalFieldsJson = extras
	}
	if original != nil {
		source.Data = original
	}
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
	if sourced {
		record.SourceCursor = proto.String(record.Cursor)
	}
	if page.snapshot {
		size = proto.Size(record)
		if len(page.response.Observations) >= maxReplaySnapshotRecords || page.size+size > maxReplaySnapshotBytes {
			page.err = ErrReplaySnapshotLimit
			return
		}
	}
	page.size += size
	page.response.Observations = append(page.response.Observations, record)
}
