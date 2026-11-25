package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

const (
	liveWindow         = 512
	liveWindowBytes    = 2 << 20
	controlWindow      = 64
	controlWindowBytes = 256 << 10
)

// observationOnlyGap identifies a skipped data interval for which the runtime
// retained every evicted permission and turn result separately.
const observationOnlyGap = "acp.observation_only_gap"

// IsObservationOnlyGap reports whether a gap emitted by this runtime skipped
// only observational data. On an unrecognized gap, callers must reconcile
// pending permission and turn evidence before resuming control.
func IsObservationOnlyGap(obs *model.Observation) bool {
	return obs.GetGap() != nil && obs.GetSourceRecord().GetType() == observationOnlyGap
}

type controlObservation struct {
	sequence    uint64
	observation *model.Observation
	size        int
}

func (s *session) signal()                  { close(s.changed); s.changed = make(chan struct{}) }
func (s *session) cursor(seq uint64) string { return s.epoch + "." + strconv.FormatUint(seq, 10) }

// emit is called with the session lock held. The bounded window is disposable
// transport state; durable reads go to the harness's native history reader.
func (s *session) emit(event *model.Observation) {
	s.position(event)
	s.retain(event, proto.Size(event))
}

func (s *session) position(event *model.Observation) {
	event.Cursor = s.cursor(s.sequence + 1)
	event.Session = s.info.Ref
	if s.turn != nil {
		event.TurnId = s.turn.id
	}
}

// emitSource keeps the original record once, at the correlated observation.
// A passive source with no semantic event is itself an extension observation.
func (s *session) emitSource(event *model.Observation, kind string, raw json.RawMessage) *model.Observation {
	return s.emitWireSource(event, kind, raw, nil)
}

func (s *session) emitWireSource(event *model.Observation, kind string, raw json.RawMessage, wire *model.WireProvenance) *model.Observation {
	event, source := withSource(event, kind, raw)
	event.WireProvenance = wire
	s.position(event)
	event.SourceCursor = proto.String(event.Cursor)
	size := boundEvidence(event, source, "live_window_limit", liveWindowBytes)
	s.retain(event, size)
	return event
}

// Prefer the original source when it fits by itself. If the source cannot
// fit, retain small envelope fields alongside its explicit omission marker.
func boundEvidence(event *model.Observation, source *model.Extension, reason string, limit int) int {
	size := proto.Size(event)
	if size <= limit {
		return size
	}
	wire := event.WireProvenance
	event.WireProvenance = nil
	if wire == nil || proto.Size(event) > limit {
		omitSource(event, source, reason, limit)
	}
	event.WireProvenance = wire
	if wire != nil && proto.Size(event) > limit {
		omitWireExtras(wire)
		if proto.Size(event) > limit {
			omitWireID(wire)
		}
		if proto.Size(event) > limit {
			if source.Type == "acp.source_omitted" {
				compactOversizedPreview(event, source, limit)
			} else {
				omitSource(event, source, reason, limit)
			}
		}
	}
	return proto.Size(event)
}

func omitSource(event *model.Observation, source *model.Extension, reason string, limit int) {
	marker, _ := json.Marshal(struct {
		SourceType string `json:"sourceType"`
		Reason     string `json:"reason"`
		ByteCount  int    `json:"byteCount"`
	}{source.Type, reason, len(source.Data)})
	source.Type, source.Data = "acp.source_omitted", marker
	compactOversizedPreview(event, source, limit)
}

func compactOversizedPreview(event *model.Observation, source *model.Extension, limit int) {
	if proto.Size(event) <= limit {
		return
	}
	if end := event.GetTurnEnded(); end != nil {
		end.Reason = "adapter returned an oversized terminal reason"
		return
	}
	// The typed preview can itself exceed the budget. Keep the passive
	// evidence or omission marker visible at this cursor, without evicting it.
	event.SourceRecord = nil
	event.Event = &model.Observation_Extension{Extension: source}
}

func withSource(event *model.Observation, kind string, raw json.RawMessage) (*model.Observation, *model.Extension) {
	source := &model.Extension{Type: kind, MediaType: "application/json", Data: raw}
	if event == nil {
		event = &model.Observation{Event: &model.Observation_Extension{Extension: source}}
	} else {
		event.SourceRecord = source
	}
	return event, source
}

func (s *session) retain(event *model.Observation, size int) {
	s.sequence++
	s.events = append(s.events, event)
	s.windowBytes += size
	for len(s.events) > liveWindow || s.windowBytes > liveWindowBytes {
		oldest := s.events[0]
		size := proto.Size(oldest)
		if isControlObservation(oldest) {
			sequence := s.sequence - uint64(len(s.events)) + 1
			s.retainControl(controlObservation{sequence: sequence, observation: oldest, size: size})
		}
		s.windowBytes -= size
		s.events[0] = nil
		s.events = s.events[1:]
	}
	s.signal()
}

func isControlObservation(obs *model.Observation) bool {
	if obs.GetInput() != nil || obs.GetTurnEnded() != nil {
		return true
	}
	switch obs.GetExtension().GetType() {
	case "acp.v2_prompt_accepted", "acp.v2_permission_dismissed":
		return true
	}
	return false
}

func (s *session) retainControl(event controlObservation) {
	s.expiredControls = append(s.expiredControls, event)
	s.controlBytes += event.size
	for len(s.expiredControls) > controlWindow || s.controlBytes > controlWindowBytes {
		oldest := &s.expiredControls[0]
		s.lostControl = oldest.sequence
		s.controlBytes -= oldest.size
		*oldest = controlObservation{}
		s.expiredControls = s.expiredControls[1:]
	}
}

type observations struct {
	ctx     context.Context
	cancel  context.CancelFunc
	session *session
	after   uint64
	gap     bool
}

func (r *Runtime) Stream(ctx context.Context, op string, req proto.Message) (agentacp.Stream, error) {
	if op != "Observe" {
		return nil, fmt.Errorf("unsupported stream %q", op)
	}
	q, ok := req.(*model.ObserveRequest)
	if !ok || q == nil {
		return nil, errors.New("invalid observation request")
	}
	s, err := r.session(q.Session)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var after uint64
	gap := false
	if q.AfterCursor != "" {
		epoch, seq, ok := strings.Cut(q.AfterCursor, ".")
		if !ok {
			return nil, errors.New("invalid observation cursor")
		}
		after, err = strconv.ParseUint(seq, 10, 64)
		if err != nil {
			return nil, errors.New("invalid observation cursor")
		}
		if epoch != s.epoch {
			gap = true
			after = 0
		} else if after > s.sequence {
			return nil, errors.New("observation cursor is ahead of this session")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	return &observations{ctx: ctx, cancel: cancel, session: s, after: after, gap: gap}, nil
}

func (o *observations) Close() { o.cancel() }
func (o *observations) Recv(dst proto.Message) error {
	s := o.session
	for {
		if err := o.ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		first := s.sequence - uint64(len(s.events))
		if o.gap || o.after < first {
			event := o.beforeWindow(first)
			s.mu.Unlock()
			proto.Reset(dst)
			proto.Merge(dst, event)
			return nil
		}
		if o.after < s.sequence {
			event := s.events[o.after-first]
			o.after++
			s.mu.Unlock()
			proto.Reset(dst)
			proto.Merge(dst, event)
			return nil
		}
		wait := s.changed
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return io.EOF
		}
		select {
		case <-o.ctx.Done():
			return o.ctx.Err()
		case <-wait:
		}
	}
}

// beforeWindow returns missing control observations in their original order.
// A cursor from another epoch cannot establish what has already been seen.
func (o *observations) beforeWindow(first uint64) *model.Observation {
	s := o.session
	recoverable := !o.gap && o.after >= s.lostControl
	o.gap = false
	through := first
	if recoverable {
		for _, event := range s.expiredControls {
			if event.sequence <= o.after {
				continue
			}
			if event.sequence == o.after+1 {
				o.after = event.sequence
				return event.observation
			}
			through = event.sequence - 1
			break
		}
	}
	o.after = through
	event := &model.Observation{Session: s.info.Ref, Cursor: s.cursor(through),
		Event: &model.Observation_Gap{Gap: &model.HistoryGap{Reason: "live observation window expired; use native history"}}}
	if recoverable {
		event.SourceRecord = &model.Extension{Type: observationOnlyGap, MediaType: "application/json", Data: []byte("{}")}
	}
	return event
}
