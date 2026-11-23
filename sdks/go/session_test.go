package agentacp_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	agentacp "agent-acp.local/sdks/go"
	pb "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type step struct {
	Operation string
	Request   json.RawMessage
	Replies   []json.RawMessage
}

func fixture(t *testing.T) []step {
	t.Helper()
	data, err := os.ReadFile("../../conformance/session.json")
	if err != nil {
		t.Fatal(err)
	}
	var steps []step
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}
	return steps
}

func decode(t *testing.T, data json.RawMessage, target proto.Message) {
	t.Helper()
	if err := protojson.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func checkMessage(t *testing.T, expected json.RawMessage, got proto.Message) {
	t.Helper()
	want := got.ProtoReflect().Type().New().Interface()
	decode(t, expected, want)
	if !proto.Equal(got, want) {
		t.Fatalf("received %v, expected %v", got, want)
	}
}

type scripted struct {
	t         *testing.T
	ctx       context.Context
	remaining []step
	stream    *stream
}

func (r *scripted) take(ctx context.Context, operation string, request proto.Message) step {
	r.t.Helper()
	if ctx != r.ctx || len(r.remaining) == 0 {
		r.t.Fatalf("unexpected operation or context for %s", operation)
	}
	s := r.remaining[0]
	r.remaining = r.remaining[1:]
	if s.Operation != operation {
		r.t.Fatalf("received %s, expected %s", operation, s.Operation)
	}
	checkMessage(r.t, s.Request, request)
	return s
}

func (r *scripted) Unary(ctx context.Context, operation string, request, response proto.Message) error {
	s := r.take(ctx, operation, request)
	return protojson.Unmarshal(s.Replies[0], response)
}

func (r *scripted) Stream(ctx context.Context, operation string, request proto.Message) (agentacp.Stream, error) {
	s := r.take(ctx, operation, request)
	r.stream = &stream{replies: s.Replies}
	return r.stream, nil
}

type stream struct {
	replies []json.RawMessage
	closed  bool
}

func (s *stream) Recv(response proto.Message) error {
	if len(s.replies) == 0 {
		return io.EOF
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return protojson.Unmarshal(r, response)
}

func (s *stream) Close() { s.closed = true }

func exerciseUnary[Request, Response proto.Message](t *testing.T, ctx context.Context, s step, req Request, call func(context.Context, Request) (Response, error)) {
	t.Helper()
	decode(t, s.Request, req)
	resp, err := call(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	checkMessage(t, s.Replies[0], resp)
}

func TestSessionContract(t *testing.T) {
	steps := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &scripted{t: t, ctx: ctx, remaining: steps}
	client := agentacp.New(r)

	for _, s := range steps {
		switch s.Operation {
		case "Open":
			exerciseUnary(t, ctx, s, &pb.OpenRequest{}, client.Open)
		case "Resume":
			exerciseUnary(t, ctx, s, &pb.ResumeRequest{}, client.Resume)
		case "Send":
			exerciseUnary(t, ctx, s, &pb.SendRequest{}, client.Send)
		case "Answer":
			exerciseUnary(t, ctx, s, &pb.AnswerRequest{}, client.Answer)
		case "Interrupt":
			exerciseUnary(t, ctx, s, &pb.InterruptRequest{}, client.Interrupt)
		case "ListSessions":
			exerciseUnary(t, ctx, s, &pb.ListSessionsRequest{}, client.ListSessions)
		case "ReadHistory":
			exerciseUnary(t, ctx, s, &pb.ReadHistoryRequest{}, client.ReadHistory)
		case "Observe":
			req := &pb.ObserveRequest{}
			decode(t, s.Request, req)
			index := 0
			for event, err := range client.Observe(ctx, req) {
				if err != nil {
					t.Fatal(err)
				}
				if index >= len(s.Replies) {
					t.Fatal("unexpected extra observation")
				}
				checkMessage(t, s.Replies[index], event)
				index++
			}
			if index != len(s.Replies) || !r.stream.closed {
				t.Fatal("observation stream did not finish cleanly")
			}
		default:
			t.Fatalf("unsupported fixture operation %q", s.Operation)
		}
	}
	if len(r.remaining) != 0 {
		t.Fatalf("%d missing runtime calls", len(r.remaining))
	}
}

func TestStopObserving(t *testing.T) {
	for _, s := range fixture(t) {
		if s.Operation != "Observe" {
			continue
		}
		ctx := context.Background()
		r := &scripted{t: t, ctx: ctx, remaining: []step{s}}
		req := &pb.ObserveRequest{}
		decode(t, s.Request, req)
		for _, err := range agentacp.New(r).Observe(ctx, req) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if !r.stream.closed || len(r.stream.replies) != len(s.Replies)-1 {
			t.Fatal("early exit should close the stream after consuming a single observation")
		}
		return
	}
	t.Fatal("missing Observe fixture")
}
