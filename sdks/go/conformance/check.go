// Package conformance verifies portable session behavior through the SDK facade.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

// Scenario describes a deterministic harness fixture. Set up the provider in a
// disposable working directory. The check answers only the named fixture
// permission with its explicitly named choice.
type Scenario struct {
	Prompt              string
	PermissionPrompt    string
	ChoiceLabel         string
	ToolResultMarker    string
	ToolResultPhase     string
	CompletionMarker    string
	InterruptPrompt     string
	InterruptReady      string
	HistoryMarker       string
	ExtensionType       string
	ExtensionDataMarker string
}

type Target struct {
	HarnessInstance  string
	WorkingDirectory string
}

// Check runs the same session rules against an embedded Go runtime or an
// executable connected through the SDK's ACP runtime. The caller owns runtime
// shutdown and should supply a bounded context.
func Check(ctx context.Context, runtime agentacp.Runtime, target Target, fixture Scenario) error {
	if runtime == nil || target.HarnessInstance == "" || !filepath.IsAbs(target.WorkingDirectory) {
		return errors.New("conformance requires a runtime, harness identity and absolute disposable workspace")
	}
	if fixture.Prompt == "" || fixture.PermissionPrompt == "" || fixture.ChoiceLabel == "" ||
		fixture.ToolResultMarker == "" || fixture.CompletionMarker == "" ||
		fixture.InterruptPrompt == "" || fixture.InterruptReady == "" {
		return errors.New("conformance fixture requires a prompt, permission, tool and completion markers, and an interrupt scenario")
	}
	if fixture.ToolResultPhase == "" {
		fixture.ToolResultPhase = "completed"
	}
	suite := check{ctx: ctx, client: agentacp.New(runtime), fixture: fixture}
	open := &model.OpenRequest{HarnessInstance: target.HarnessInstance, WorkingDirectory: target.WorkingDirectory}
	first, err := suite.open(open, "first")
	if err != nil {
		return err
	}
	second, err := suite.open(open, "second")
	if err != nil {
		return err
	}
	if proto.Equal(first.Ref, second.Ref) {
		return errors.New("two opens returned the same session")
	}

	a, err := suite.start(first.Ref, "conformance-first")
	if err != nil {
		return err
	}
	b, err := suite.start(second.Ref, "conformance-second")
	if err != nil {
		return err
	}
	if a.request.RequestId == b.request.RequestId {
		// The ID is session-scoped, so reuse is legal. A foreign request can
		// only be tested when the two providers issue distinct IDs.
		if _, err := suite.client.Answer(ctx, answer(b, "conformance-wrong-option", b.request.RequestId, b.choice+"-foreign")); err == nil {
			return errors.New("adapter accepted an option outside the second session's pending request")
		}
	} else if _, err := suite.client.Answer(ctx, answer(b, "conformance-crossed", a.request.RequestId, a.choice)); err == nil {
		return errors.New("adapter accepted a pending request from the first session in the second")
	}
	if err := suite.finish(a); err != nil {
		return err
	}
	if err := suite.finish(b); err != nil {
		return err
	}
	if err := suite.list(target, first, second); err != nil {
		return err
	}
	if err := suite.media(first, a.cursor); err != nil {
		return err
	}
	firstCaps, secondCaps := first.GetCapabilities(), second.GetCapabilities()
	if firstCaps.GetCanPromptImage() != secondCaps.GetCanPromptImage() || firstCaps.GetCanPromptAudio() != secondCaps.GetCanPromptAudio() {
		if err := suite.media(second, b.cursor); err != nil {
			return err
		}
	}
	if first.GetCapabilities().GetCanResume() {
		resumed, err := suite.client.Resume(ctx, &model.ResumeRequest{Session: first.Ref})
		if err != nil {
			return fmt.Errorf("resume fixture session: %w", err)
		}
		if !proto.Equal(resumed.GetSession().GetRef(), first.Ref) {
			return errors.New("resume changed the provider session identity")
		}
	}
	if err := suite.interrupt(second); err != nil {
		return err
	}
	return suite.history(first)
}

type check struct {
	ctx     context.Context
	client  *agentacp.Sessions
	fixture Scenario
}

func (s check) open(request *model.OpenRequest, label string) (*model.Session, error) {
	response, err := s.client.Open(s.ctx, request)
	if err != nil {
		return nil, fmt.Errorf("open %s session: %w", label, err)
	}
	session := response.GetSession()
	if session.GetRef().GetSessionId() == "" || session.GetRef().GetHarnessInstance() != request.HarnessInstance {
		return nil, fmt.Errorf("%s session has an invalid identity", label)
	}
	if !session.GetCapabilities().GetCanAnswer() {
		return nil, fmt.Errorf("%s session does not advertise permission answers", label)
	}
	return session, nil
}

type pending struct {
	ref     *model.SessionRef
	id      string
	cursor  string
	seen    map[string]bool
	request *model.InputRequest
	choice  string
}

func (p *pending) observe(item *model.Observation) error {
	if !proto.Equal(item.GetSession(), p.ref) || item.GetTurnId() != p.id {
		return fmt.Errorf("%s received an observation addressed to another session or turn", p.id)
	}
	cursor := item.GetCursor()
	if item.GetGap() != nil || cursor == "" || p.seen[cursor] {
		return fmt.Errorf("%s received a gap, empty cursor or duplicate cursor", p.id)
	}
	p.seen[cursor], p.cursor = true, cursor
	return nil
}

func (s check) start(ref *model.SessionRef, id string) (*pending, error) {
	request := &model.SendRequest{Session: ref, OperationId: id, Text: s.fixture.Prompt}
	response, err := s.client.Send(s.ctx, request)
	if err := accepted(response.GetReceipt(), id, err); err != nil {
		return nil, err
	}
	retry, err := s.client.Send(s.ctx, request)
	if err := accepted(retry.GetReceipt(), id, err); err != nil {
		return nil, fmt.Errorf("retry the same send: %w", err)
	}
	changed := proto.Clone(request).(*model.SendRequest)
	changed.Text += "\nchanged intent"
	if _, err := s.client.Send(s.ctx, changed); err == nil {
		return nil, fmt.Errorf("%s accepted a different prompt under the same operation ID", id)
	}
	p := &pending{ref: ref, id: id, seen: make(map[string]bool)}
	var literal bool
	for item, err := range s.client.Observe(s.ctx, &model.ObserveRequest{Session: ref}) {
		if err != nil {
			return nil, fmt.Errorf("observe %s: %w", id, err)
		}
		if err := p.observe(item); err != nil {
			return nil, err
		}
		text := item.GetText()
		literal = literal || (text.GetSpeaker() == model.Speaker_SPEAKER_USER && text.GetText() == s.fixture.Prompt)
		if input := item.GetInput(); input != nil {
			if input.Prompt != s.fixture.PermissionPrompt || input.RequestId == "" || !literal {
				return nil, fmt.Errorf("%s did not retain the literal prompt and expected permission", id)
			}
			for _, option := range input.Choices {
				if option.Label == s.fixture.ChoiceLabel && option.Id != "" {
					if p.choice != "" {
						return nil, fmt.Errorf("%s has an ambiguous fixture choice", id)
					}
					p.choice = option.Id
				}
			}
			if p.choice == "" {
				return nil, fmt.Errorf("%s did not offer the fixture choice", id)
			}
			p.request = input
			return p, nil
		}
		if item.GetTurnEnded() != nil {
			break
		}
	}
	return nil, fmt.Errorf("%s ended before the fixture permission", id)
}

func answer(p *pending, operation, id, choice string) *model.AnswerRequest {
	return &model.AnswerRequest{Session: p.ref, OperationId: operation, RequestId: id,
		Answer: &model.AnswerRequest_OptionId{OptionId: choice}}
}

func (s check) finish(p *pending) error {
	id := p.id + "-reply"
	request := answer(p, id, p.request.RequestId, p.choice)
	for range 2 {
		response, err := s.client.Answer(s.ctx, request)
		if err := accepted(response.GetReceipt(), id, err); err != nil {
			return err
		}
	}
	changed := answer(p, id, p.request.RequestId, p.choice+"-changed")
	if _, err := s.client.Answer(s.ctx, changed); err == nil {
		return fmt.Errorf("%s accepted a conflicting reply for the same operation", p.id)
	}
	var tool, extension bool
	var text strings.Builder
	for item, err := range s.client.Observe(s.ctx, &model.ObserveRequest{Session: p.ref, AfterCursor: p.cursor}) {
		if err != nil {
			return fmt.Errorf("reconnect observer for %s: %w", p.id, err)
		}
		if err := p.observe(item); err != nil {
			return err
		}
		activity := item.GetTool()
		tool = tool || (activity.GetCallId() != "" && activity.GetPhase() == s.fixture.ToolResultPhase &&
			strings.Contains(activity.GetSummary(), s.fixture.ToolResultMarker))
		if body := item.GetText(); body.GetSpeaker() == model.Speaker_SPEAKER_ASSISTANT {
			text.WriteString(body.GetText())
		}
		source := item.GetExtension()
		extension = extension || (source != nil && source.Type == s.fixture.ExtensionType &&
			strings.Contains(string(source.Data), s.fixture.ExtensionDataMarker))
		if end := item.GetTurnEnded(); end != nil {
			if end.Reason != "end_turn" || !tool || !strings.Contains(text.String(), s.fixture.CompletionMarker) ||
				(s.fixture.ExtensionType != "" && !extension) {
				return fmt.Errorf("%s completed without the expected tool result, reply confirmation or extension", p.id)
			}
			return nil
		}
		if item.GetInput() != nil {
			return fmt.Errorf("%s repeated a resolved permission", p.id)
		}
	}
	return fmt.Errorf("%s disconnected before completion", p.id)
}

func (s check) interrupt(session *model.Session) error {
	if !session.GetCapabilities().GetCanInterrupt() {
		return errors.New("fixture session did not advertise interruption")
	}
	const id = "conformance-interrupt"
	response, err := s.client.Send(s.ctx, &model.SendRequest{Session: session.Ref, OperationId: id, Text: s.fixture.InterruptPrompt})
	if err := accepted(response.GetReceipt(), id, err); err != nil {
		return err
	}
	p := &pending{ref: session.Ref, id: id, seen: make(map[string]bool)}
	var text strings.Builder
	var requested bool
	for item, err := range s.client.Observe(s.ctx, &model.ObserveRequest{Session: session.Ref}) {
		if err != nil {
			return fmt.Errorf("observe interrupted turn: %w", err)
		}
		if item.GetTurnId() != id {
			if !proto.Equal(item.GetSession(), session.Ref) || item.GetGap() != nil {
				return errors.New("interrupt observation escaped its session")
			}
			continue // This is retained output from the completed fixture turn.
		}
		if err := p.observe(item); err != nil {
			return err
		}
		if item.GetText().GetSpeaker() == model.Speaker_SPEAKER_ASSISTANT {
			text.WriteString(item.GetText().Text)
		}
		if !requested && strings.Contains(text.String(), s.fixture.InterruptReady) {
			if _, err := s.client.Interrupt(s.ctx, &model.InterruptRequest{
				Session: session.Ref, TurnId: "conformance-second", OperationId: "conformance-stale-stop",
			}); err == nil {
				return errors.New("a stale interrupt affected the active turn")
			}
			const op = "conformance-stop"
			reply, err := s.client.Interrupt(s.ctx, &model.InterruptRequest{Session: session.Ref, TurnId: id, OperationId: op})
			if err := accepted(reply.GetReceipt(), op, err); err != nil {
				return err
			}
			requested = true
		}
		if end := item.GetTurnEnded(); end != nil {
			if !requested || end.Reason != "cancelled" {
				return errors.New("targeted stop did not produce a cancelled turn")
			}
			return nil
		}
	}
	return errors.New("interrupt stream ended before cancellation")
}

func (s check) history(session *model.Session) error {
	req := &model.ReadHistoryRequest{Session: session.Ref, Limit: 1}
	advertised := session.GetCapabilities().GetCanReadHistory()
	if !advertised {
		if _, err := s.client.ReadHistory(s.ctx, req); err == nil {
			return errors.New("adapter served unadvertised native history")
		}
		if s.fixture.HistoryMarker != "" {
			return errors.New("fixture expects passive history but the adapter does not advertise it")
		}
		return nil
	}
	if s.fixture.HistoryMarker == "" {
		return errors.New("advertised history requires a retained fixture marker")
	}
	seen := make(map[string]bool)
	var found bool
	for {
		page, err := s.client.ReadHistory(s.ctx, req)
		if err != nil {
			return fmt.Errorf("read passive history: %w", err)
		}
		if len(page.GetObservations()) > int(req.Limit) {
			return errors.New("history exceeded its requested page size")
		}
		for _, item := range page.Observations {
			if !proto.Equal(item.GetSession(), session.Ref) || item.GetInput() != nil {
				return errors.New("passive history returned a foreign session or an actionable request")
			}
			found = found || strings.Contains(item.GetText().GetText(), s.fixture.HistoryMarker) ||
				strings.Contains(item.GetTool().GetSummary(), s.fixture.HistoryMarker) ||
				strings.Contains(string(item.GetExtension().GetData()), s.fixture.HistoryMarker)
		}
		if page.GetNextCursor() == "" {
			break
		}
		if seen[page.NextCursor] {
			return errors.New("history repeated its pagination cursor")
		}
		req.Cursor = page.NextCursor
		seen[req.Cursor] = true
	}
	if !found {
		return errors.New("passive history omitted its retained fixture marker")
	}
	return nil
}

func accepted(receipt *model.Receipt, operation string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if receipt.GetOperationId() != operation {
		return fmt.Errorf("%s returned a receipt for a different command", operation)
	}
	switch receipt.GetStatus() {
	case model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, model.DeliveryStatus_DELIVERY_STATUS_DELIVERED:
		return nil
	default:
		return fmt.Errorf("%s returned an unsuccessful receipt: %s", operation, receipt.GetStatus())
	}
}
