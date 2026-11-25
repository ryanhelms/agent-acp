package runtime

import (
	"context"
	"strconv"
	"testing"

	model "agent-acp.local/sdks/go/model"
)

func TestGapRecoveryPreservesOrderedPermissionAndTurnEvidence(t *testing.T) {
	s := newSession()
	s.info = &model.Session{Ref: &model.SessionRef{HarnessInstance: "fixture", SessionId: "s"}}
	s.turn = &turn{id: "original"}
	text := func(value string) {
		s.emit(&model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Text: value}}})
	}
	text("expired text before approval")
	s.emit(&model.Observation{Event: &model.Observation_Input{Input: &model.InputRequest{RequestId: "must-retain"}}})
	text("expired text after approval")
	s.emitSource(&model.Observation{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: "cancelled"}}},
		"acp.prompt_result", []byte(`{"stopReason":"cancelled"}`))
	s.turn = nil
	for i := range liveWindow {
		text(strconv.Itoa(i))
	}
	stream := &observations{ctx: context.Background(), session: s}
	if gap := recv(t, stream); !IsObservationOnlyGap(gap) || gap.Cursor != s.cursor(1) {
		t.Fatalf("wrong recovery gap before permission: %v", gap)
	}
	if permission := recv(t, stream); permission.GetInput().GetRequestId() != "must-retain" || permission.TurnId != "original" || permission.Cursor != s.cursor(2) {
		t.Fatalf("lost approval evidence: %v", permission)
	}
	if gap := recv(t, stream); !IsObservationOnlyGap(gap) || gap.Cursor != s.cursor(3) {
		t.Fatalf("wrong recovery gap before turn result: %v", gap)
	}
	if end := recv(t, stream); end.GetTurnEnded().GetReason() != "cancelled" ||
		end.GetSourceRecord().GetType() != "acp.prompt_result" || end.TurnId != "original" || end.Cursor != s.cursor(4) {
		t.Fatalf("lost terminal receipt: %v", end)
	}
	if next := recv(t, stream); next.GetText().GetText() != "0" || next.Cursor != s.cursor(5) {
		t.Fatalf("wrong continuation through live window: %v", next)
	}
}

func TestGapRecoveryWithholdsCertaintyAfterControlEvictionOrEpochChange(t *testing.T) {
	s := newSession()
	s.info = &model.Session{Ref: &model.SessionRef{HarnessInstance: "fixture", SessionId: "s"}}
	for i := range controlWindow + 1 {
		s.emit(&model.Observation{Event: &model.Observation_Input{Input: &model.InputRequest{RequestId: strconv.Itoa(i)}}})
	}
	for range liveWindow {
		s.emit(&model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Text: "retained"}}})
	}
	for _, observer := range []*observations{
		{ctx: context.Background(), session: s},
		{ctx: context.Background(), session: s, gap: true},
	} {
		gap := recv(t, observer)
		if gap.GetGap() == nil || IsObservationOnlyGap(gap) {
			t.Fatalf("reported control continuity without evidence: %v", gap)
		}
	}
	observer := &observations{ctx: context.Background(), session: s, after: 1}
	if permission := recv(t, observer); permission.GetInput().GetRequestId() != "1" {
		t.Fatalf("resume after safely observed control failed: %v", permission)
	}
}
