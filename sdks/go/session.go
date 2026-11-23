// Code generated from agentacp/v1/session.proto. DO NOT EDIT.

package agentacp

import (
	"context"
	"errors"
	"io"
	"iter"

	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

// Runtime implements the portable session operations on the execution host.
// Unary fills response. Stream and its reads must observe ctx cancellation.
type Runtime interface {
	Unary(ctx context.Context, operation string, request, response proto.Message) error
	Stream(ctx context.Context, operation string, request proto.Message) (Stream, error)
}

// Stream fills one response per Recv, returns io.EOF on completion, and
// releases the subscription on Close. Closing a subscription preserves its session.
type Stream interface {
	Recv(response proto.Message) error
	Close()
}

type Sessions struct {
	runtime Runtime
}

func New(runtime Runtime) *Sessions {
	return &Sessions{runtime: runtime}
}

func unary[T proto.Message](ctx context.Context, runtime Runtime, operation string, request proto.Message, response T) (T, error) {
	if err := runtime.Unary(ctx, operation, request, response); err != nil {
		var zero T
		return zero, err
	}
	return response, nil
}

func stream[T proto.Message](ctx context.Context, runtime Runtime, operation string, request proto.Message, newItem func() T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		reader, err := runtime.Stream(ctx, operation, request)
		if err != nil {
			yield(zero, err)
			return
		}
		defer reader.Close()
		for {
			item := newItem()
			err := reader.Recv(item)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(zero, err)
				return
			}
			if !yield(item, nil) {
				return
			}
		}
	}
}

func (s *Sessions) Open(ctx context.Context, request *model.OpenRequest) (*model.OpenResponse, error) {
	return unary(ctx, s.runtime, "Open", request, new(model.OpenResponse))
}

func (s *Sessions) Resume(ctx context.Context, request *model.ResumeRequest) (*model.ResumeResponse, error) {
	return unary(ctx, s.runtime, "Resume", request, new(model.ResumeResponse))
}

func (s *Sessions) Send(ctx context.Context, request *model.SendRequest) (*model.SendResponse, error) {
	return unary(ctx, s.runtime, "Send", request, new(model.SendResponse))
}

func (s *Sessions) Observe(ctx context.Context, request *model.ObserveRequest) iter.Seq2[*model.Observation, error] {
	return stream(ctx, s.runtime, "Observe", request, func() *model.Observation { return new(model.Observation) })
}

func (s *Sessions) Answer(ctx context.Context, request *model.AnswerRequest) (*model.AnswerResponse, error) {
	return unary(ctx, s.runtime, "Answer", request, new(model.AnswerResponse))
}

func (s *Sessions) Interrupt(ctx context.Context, request *model.InterruptRequest) (*model.InterruptResponse, error) {
	return unary(ctx, s.runtime, "Interrupt", request, new(model.InterruptResponse))
}

func (s *Sessions) ListSessions(ctx context.Context, request *model.ListSessionsRequest) (*model.ListSessionsResponse, error) {
	return unary(ctx, s.runtime, "ListSessions", request, new(model.ListSessionsResponse))
}

func (s *Sessions) ReadHistory(ctx context.Context, request *model.ReadHistoryRequest) (*model.ReadHistoryResponse, error) {
	return unary(ctx, s.runtime, "ReadHistory", request, new(model.ReadHistoryResponse))
}
