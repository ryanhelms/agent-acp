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
	liveWindow      = 512
	liveWindowBytes = 2 << 20
)

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
	event, source := withSource(event, kind, raw)
	s.position(event)
	event.SourceCursor = proto.String(event.Cursor)
	if size := proto.Size(event); size <= liveWindowBytes {
		s.retain(event, size)
		return event
	}
	// A source bound is independent of replay continuity. Keep the semantic
	// event at the same cursor and make only its oversized evidence unavailable.
	omitSource(event, source, "live_window_limit", liveWindowBytes)
	s.retain(event, proto.Size(event))
	return event
}

func omitSource(event *model.Observation, source *model.Extension, reason string, limit int) {
	updateParams := source.Type == ACPUpdateParamsSource
	marker, _ := json.Marshal(struct {
		SourceType string `json:"sourceType"`
		Reason     string `json:"reason"`
		ByteCount  int    `json:"byteCount"`
	}{source.Type, reason, len(source.Data)})
	source.Type, source.Data = "acp.source_omitted", marker
	if updateParams && proto.Size(event) > limit {
		// Even the typed preview cannot fit. Keep the gap at its own cursor
		// without evicting the whole observation window.
		event.SourceRecord = nil
		event.Event = &model.Observation_Extension{Extension: source}
	}
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
		s.windowBytes -= proto.Size(s.events[0])
		s.events[0] = nil
		s.events = s.events[1:]
	}
	s.signal()
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
			o.gap = false
			o.after = first
			event := &model.Observation{Session: s.info.Ref, Cursor: s.cursor(first), Event: &model.Observation_Gap{Gap: &model.HistoryGap{Reason: "live observation window expired; use native history"}}}
			proto.Reset(dst)
			proto.Merge(dst, event)
			s.mu.Unlock()
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
