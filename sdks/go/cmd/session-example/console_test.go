package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

type testRuntime struct {
	unary func(string, proto.Message, proto.Message) error
}

func (r testRuntime) Unary(_ context.Context, op string, request, response proto.Message) error {
	return r.unary(op, request, response)
}

func (testRuntime) Stream(context.Context, string, proto.Message) (agentacp.Stream, error) {
	return nil, errors.New("unexpected live observation")
}

func TestCommandsUseLiteralInputAndExplicitTargets(t *testing.T) {
	ref := &model.SessionRef{HarnessInstance: "local", SessionId: "native"}
	text := " $HOME\n  'value'; $() 🧪\n"
	cases := []struct {
		op      string
		command command
		want    proto.Message
	}{
		{"Send", command{Op: "send", ID: "s", Text: text}, &model.SendRequest{Session: ref, OperationId: "s", Text: text}},
		{"Answer", command{Op: "answer", ID: "a", Request: "req", Option: "deny-once"}, &model.AnswerRequest{Session: ref, OperationId: "a", RequestId: "req", Answer: &model.AnswerRequest_OptionId{OptionId: "deny-once"}}},
		{"Interrupt", command{Op: "interrupt", ID: "i", Turn: "s"}, &model.InterruptRequest{Session: ref, OperationId: "i", TurnId: "s"}},
	}
	for _, test := range cases {
		t.Run(test.op, func(t *testing.T) {
			var called bool
			client := agentacp.New(testRuntime{func(op string, req, resp proto.Message) error {
				called = true
				if op != test.op || !proto.Equal(req, test.want) {
					t.Fatalf("unexpected %s request %v", op, req)
				}
				return nil
			}})
			if _, err := execute(context.Background(), client, ref, test.command); err != nil || !called {
				t.Fatalf("called=%v, err=%v", called, err)
			}
		})
	}
}

func TestHistoryIsPassiveAcrossPages(t *testing.T) {
	ref := &model.SessionRef{HarnessInstance: "remote", SessionId: "native"}
	var calls int
	client := agentacp.New(testRuntime{func(op string, req, resp proto.Message) error {
		request := req.(*model.ReadHistoryRequest)
		if op != "ReadHistory" || !proto.Equal(request.Session, ref) {
			t.Fatalf("unexpected %s request %v", op, req)
		}
		response := resp.(*model.ReadHistoryResponse)
		switch calls {
		case 0:
			if request.Cursor != "" {
				t.Fatalf("first cursor %q", request.Cursor)
			}
			response.NextCursor = "page2"
			response.Observations = []*model.Observation{{Session: ref, Event: &model.Observation_Input{Input: &model.InputRequest{RequestId: "old"}}}}
		case 1:
			if request.Cursor != "page2" {
				t.Fatalf("next cursor %q", request.Cursor)
			}
		default:
			t.Fatal("unexpected extra page")
		}
		calls++
		return nil
	}})
	var output bytes.Buffer
	if err := readHistory(context.Background(), client, ref, json.NewEncoder(&output)); err != nil {
		t.Fatal(err)
	}
	var observed record
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil || observed.Type != "history" || calls != 2 {
		t.Fatalf("calls=%d, record=%+v, err=%v", calls, observed, err)
	}
}

type liveTestRuntime struct {
	events chan *model.Observation
	send   func(*model.SendRequest)
}

func (r liveTestRuntime) Unary(_ context.Context, op string, req, resp proto.Message) error {
	if op != "Send" {
		return errors.New("unexpected SDK operation " + op)
	}
	send := req.(*model.SendRequest)
	r.send(send)
	resp.(*model.SendResponse).Receipt = &model.Receipt{OperationId: send.OperationId, Status: model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED}
	return nil
}

func (r liveTestRuntime) Stream(ctx context.Context, _ string, _ proto.Message) (agentacp.Stream, error) {
	return eventReader{ctx, r.events}, nil
}

type eventReader struct {
	ctx    context.Context
	events <-chan *model.Observation
}

func (r eventReader) Recv(response proto.Message) error {
	select {
	case <-r.ctx.Done():
		return io.EOF
	case event := <-r.events:
		proto.Merge(response, event)
		return nil
	}
}

func (eventReader) Close() {}

func TestEOFWaitsForOutcomeAndNeverAnswers(t *testing.T) {
	for _, decision := range []bool{false, true} {
		ref := &model.SessionRef{HarnessInstance: "local", SessionId: "native"}
		events := make(chan *model.Observation, 1)
		client := agentacp.New(liveTestRuntime{events, func(req *model.SendRequest) {
			event := &model.Observation{Session: ref, TurnId: req.OperationId}
			if decision {
				event.Event = &model.Observation_Input{Input: &model.InputRequest{RequestId: "req"}}
			} else {
				event.Event = &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: "end_turn"}}
			}
			events <- event
		}})
		var output bytes.Buffer
		err := interact(context.Background(), client, ref, strings.NewReader(""), json.NewEncoder(&output), []byte("hello\n"))
		if decision != (err != nil) || !strings.Contains(output.String(), `"type":"observation"`) {
			t.Fatalf("decision=%v err=%v output=%s", decision, err, output.String())
		}
	}
}
