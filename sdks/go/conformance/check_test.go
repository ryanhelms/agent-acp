package conformance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

var fixture = Scenario{
	Prompt:           "  Read the fixture. Preserve $HOME and \"quotes\".\n",
	PermissionPrompt: "Allow reading file?", ChoiceLabel: "Allow once",
	ToolResultMarker: "fixture bytes", CompletionMarker: "permission received",
	InterruptPrompt: "wait", InterruptReady: "waiting",
	HistoryMarker: "fixture bytes", ExtensionType: "fixture_resource", ExtensionDataMarker: "resource unavailable",
}

func TestEmbeddedAdapterConformance(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		name := "passes"
		if incomplete {
			name = "rejects the missing result"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := &embedded{sessions: make(map[string]*memorySession), incomplete: incomplete}
			err := Check(ctx, r, Target{HarnessInstance: "embedded", WorkingDirectory: t.TempDir()}, fixture)
			if incomplete == (err == nil) {
				t.Fatalf("embedded adapter with incomplete=%t returned %v", incomplete, err)
			}
			if !incomplete && (r.opens != 2 || r.historyReads != 2 || r.messages != 3) {
				t.Fatalf("operations were duplicated or history was not paged: %+v", r)
			}
		})
	}
}

func TestAdvertisedListingAndMedia(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		list, image, audio, omitListed, badMedia bool
		want                                     string
	}{
		{name: "both media and paged listing", list: true, image: true, audio: true},
		{name: "only image", image: true},
		{name: "only audio", audio: true},
		{name: "incomplete advertised list", list: true, omitListed: true, want: "omitted"},
		{name: "advertised image corrupted", image: true, badMedia: true, want: "exact media bytes"},
		{name: "advertised audio corrupted", audio: true, badMedia: true, want: "exact media bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r := &embedded{sessions: make(map[string]*memorySession), list: tc.list, image: tc.image, audio: tc.audio, omitListed: tc.omitListed, badMedia: tc.badMedia}
			err := Check(ctx, r, Target{HarnessInstance: "compiled", WorkingDirectory: t.TempDir()}, fixture)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMessages := 3
			if tc.image {
				wantMessages++
			}
			if tc.audio {
				wantMessages++
			}
			if r.messages != wantMessages || (tc.list && r.listReads != 2) {
				t.Fatalf("media turns=%d, list reads=%d", r.messages-3, r.listReads)
			}
		})
	}
}

type embedded struct {
	sessions                                 map[string]*memorySession
	opens, historyReads, messages            int
	incomplete                               bool
	list, image, audio, omitListed, badMedia bool
	listReads                                int
}

type memorySession struct {
	ref     *model.SessionRef
	turn    string
	request string
	ops     map[string]proto.Message
	events  []*model.Observation
}

func (r *embedded) Unary(_ context.Context, op string, request, reply proto.Message) error {
	switch q := request.(type) {
	case *model.OpenRequest:
		r.opens++
		ref := &model.SessionRef{HarnessInstance: q.HarnessInstance, SessionId: fmt.Sprint(r.opens)}
		r.sessions[ref.SessionId] = &memorySession{ref: ref, ops: make(map[string]proto.Message)}
		reply.(*model.OpenResponse).Session = &model.Session{Ref: ref, Capabilities: &model.Capabilities{
			CanAnswer: true, CanInterrupt: true, CanReadHistory: true, CanListSessions: r.list, CanPromptImage: r.image, CanPromptAudio: r.audio,
		}}
	case *model.ListSessionsRequest:
		r.listReads++
		if !r.list {
			return errors.New("listing unavailable")
		}
		page := reply.(*model.ListSessionsResponse)
		switch q.Cursor {
		case "":
			page.Sessions = []*model.Session{{Ref: r.sessions["1"].ref}}
			if !r.omitListed {
				page.NextCursor = "native-list-page"
			}
		case "native-list-page":
			page.Sessions = []*model.Session{{Ref: r.sessions["2"].ref}}
		default:
			return errors.New("unexpected list cursor")
		}
	case *model.SendRequest:
		s := r.sessions[q.GetSession().GetSessionId()]
		if len(q.Parts) != 0 {
			if len(q.Parts) != 2 {
				return errors.New("invalid fixture media")
			}
			var m *model.PromptMedia
			switch q.Parts[0].GetText() {
			case "Fingerprint the attached conformance image.":
				if !r.image {
					return errors.New("images unavailable")
				}
				m = q.Parts[1].GetImage()
				if m.GetMimeType() != "image/png" {
					return errors.New("expected a PNG")
				}
			case "Fingerprint the attached conformance audio.":
				if !r.audio {
					reply.(*model.SendResponse).Receipt = &model.Receipt{OperationId: q.OperationId, Status: model.DeliveryStatus_DELIVERY_STATUS_UNSUPPORTED}
					return nil
				}
				m = q.Parts[1].GetAudio()
				if m.GetMimeType() != "audio/wav" {
					return errors.New("expected a WAV")
				}
			default:
				return errors.New("unexpected media prompt")
			}
			r.messages++
			s.turn = q.OperationId
			data := m.GetData()
			if r.badMedia {
				data = nil
			}
			s.emit(text(model.Speaker_SPEAKER_ASSISTANT, fmt.Sprintf("sha256:%x", sha256.Sum256(data))))
			s.emit(ended("end_turn"))
			reply.(*model.SendResponse).Receipt = receipt(q.OperationId)
			return nil
		}
		seen, err := s.once(q.OperationId, request)
		if err != nil {
			return err
		}
		reply.(*model.SendResponse).Receipt = receipt(q.OperationId)
		if seen {
			return nil
		}
		r.messages++
		s.turn = q.OperationId
		s.emit(text(model.Speaker_SPEAKER_USER, q.Text))
		switch q.Text {
		case fixture.Prompt:
			s.request = "question-" + s.ref.SessionId
			s.emit(&model.Observation{Event: &model.Observation_Input{Input: &model.InputRequest{
				RequestId: s.request, Prompt: fixture.PermissionPrompt, Choices: []*model.Choice{{Id: "once", Label: fixture.ChoiceLabel}},
			}}})
		case fixture.InterruptPrompt:
			s.emit(text(model.Speaker_SPEAKER_ASSISTANT, fixture.InterruptReady))
		default:
			return errors.New("unexpected fixture prompt")
		}
	case *model.AnswerRequest:
		s := r.sessions[q.GetSession().GetSessionId()]
		if q.RequestId != "question-"+s.ref.SessionId || q.GetOptionId() != "once" {
			return errors.New("wrong fixture permission")
		}
		seen, err := s.once(q.OperationId, request)
		if err != nil {
			return err
		}
		reply.(*model.AnswerResponse).Receipt = receipt(q.OperationId)
		if seen {
			return nil
		}
		if s.request != q.RequestId {
			return errors.New("fixture permission is already settled")
		}
		s.request = ""
		if !r.incomplete {
			s.emit(&model.Observation{Event: &model.Observation_Tool{Tool: &model.ToolActivity{
				CallId: "read-1", Phase: "completed", Summary: fixture.ToolResultMarker,
			}}})
		}
		s.emit(&model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{
			Type: fixture.ExtensionType, Data: []byte(fixture.ExtensionDataMarker),
		}}})
		s.emit(text(model.Speaker_SPEAKER_ASSISTANT, fixture.CompletionMarker))
		s.emit(ended("end_turn"))
	case *model.InterruptRequest:
		s := r.sessions[q.GetSession().GetSessionId()]
		if s.turn != q.TurnId {
			return errors.New("wrong fixture turn")
		}
		s.emit(ended("cancelled"))
		reply.(*model.InterruptResponse).Receipt = receipt(q.OperationId)
	case *model.ReadHistoryRequest:
		r.historyReads++
		out := reply.(*model.ReadHistoryResponse)
		switch q.Cursor {
		case "":
			out.Observations = []*model.Observation{text(model.Speaker_SPEAKER_USER, fixture.Prompt)}
			out.NextCursor = "retained-position"
		case "retained-position":
			out.Observations = []*model.Observation{text(model.Speaker_SPEAKER_ASSISTANT, fixture.HistoryMarker)}
		default:
			return errors.New("unexpected history cursor")
		}
		out.Observations[0].Session = q.Session
	default:
		return fmt.Errorf("unimplemented fixture operation: %s", op)
	}
	return nil
}

func (r *embedded) Stream(_ context.Context, op string, request proto.Message) (agentacp.Stream, error) {
	if op != "Observe" {
		return nil, errors.New("unexpected stream")
	}
	q := request.(*model.ObserveRequest)
	s := r.sessions[q.GetSession().GetSessionId()]
	reader := &memoryStream{s: s}
	if q.AfterCursor != "" {
		for i, event := range s.events {
			if event.Cursor == q.AfterCursor {
				reader.next = i + 1
				return reader, nil
			}
		}
		return nil, errors.New("invalid fixture cursor")
	}
	return reader, nil
}

func (s *memorySession) once(id string, request proto.Message) (bool, error) {
	if before, ok := s.ops[id]; ok {
		if !proto.Equal(before, request) {
			return false, errors.New("operation ID was reused with different input")
		}
		return true, nil
	}
	s.ops[id] = proto.Clone(request)
	return false, nil
}

func (s *memorySession) emit(event *model.Observation) {
	event.Session, event.TurnId = s.ref, s.turn
	event.Cursor = fmt.Sprintf("%s:%d", s.ref.SessionId, len(s.events)+1)
	s.events = append(s.events, event)
}

type memoryStream struct {
	s    *memorySession
	next int
}

func (s *memoryStream) Recv(reply proto.Message) error {
	if s.next == len(s.s.events) {
		return io.EOF
	}
	proto.Merge(reply, s.s.events[s.next])
	s.next++
	return nil
}

func (*memoryStream) Close() {}

func text(speaker model.Speaker, value string) *model.Observation {
	return &model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: speaker, Text: value}}}
}

func ended(reason string) *model.Observation {
	return &model.Observation{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: reason}}}
}

func receipt(id string) *model.Receipt {
	return &model.Receipt{OperationId: id, Status: model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED}
}
