package runtime

import (
	"context"
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
	s.sequence++
	event.Cursor = s.cursor(s.sequence)
	event.Session = s.info.Ref
	if s.turn != nil {
		event.TurnId = s.turn.id
	}
	s.events = append(s.events, event)
	s.windowBytes += proto.Size(event)
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
