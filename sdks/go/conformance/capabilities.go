package conformance

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	model "agent-acp.local/sdks/go/model"
)

// List after the two fixture turns finish so journals which materialize on
// first completion can participate. Other retained sessions are permitted.
func (s check) list(target Target, first, second *model.Session) error {
	advertised := first.GetCapabilities().GetCanListSessions()
	if advertised != second.GetCapabilities().GetCanListSessions() {
		return errors.New("two sessions in the same harness disagree about session listing")
	}
	request := &model.ListSessionsRequest{HarnessInstance: target.HarnessInstance, WorkingDirectory: target.WorkingDirectory, Limit: 1}
	seenPages := make(map[string]bool)
	seenSessions := make(map[string]bool)
	for {
		page, err := s.client.ListSessions(s.ctx, request)
		if !advertised {
			if err == nil {
				return errors.New("adapter served an unadvertised session listing")
			}
			return s.ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("advertised session listing failed: %w", err)
		}
		if page == nil || len(page.Sessions) > int(request.Limit) {
			return errors.New("session listing returned an invalid page or exceeded the requested limit")
		}
		for _, session := range page.Sessions {
			ref := session.GetRef()
			if ref.GetHarnessInstance() != target.HarnessInstance || ref.GetSessionId() == "" || seenSessions[ref.SessionId] {
				return errors.New("session listing returned a foreign, empty or repeated identity")
			}
			seenSessions[ref.SessionId] = true
		}
		if page.NextCursor == "" {
			break
		}
		if seenPages[page.NextCursor] {
			return errors.New("session listing repeated its pagination cursor")
		}
		seenPages[page.NextCursor] = true
		request.Cursor = page.NextCursor
	}
	if !seenSessions[first.Ref.SessionId] || !seenSessions[second.Ref.SessionId] {
		return errors.New("session listing omitted one or both fixture sessions in the selected workspace")
	}
	return nil
}

// The media proof uses standard prompt parts. The fixture answers with a hash
// of the decoded media it actually received. The text never gives it the hash.
func (s check) media(session *model.Session, cursor string) error {
	probes := []struct {
		kind, mime, data string
		supported        bool
	}{
		{"image", "image/png", "iVBORw0KGgoAAAANSUhEUgAAAAQAAAAECAIAAAAmkwkpAAAAEElEQVR4nGMQLC+HIwbiOACYQw/x52aSlAAAAABJRU5ErkJggg==", session.GetCapabilities().GetCanPromptImage()},
		{"audio", "audio/wav", "UklGRiwAAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQgAAACAgYSBgH98fw==", session.GetCapabilities().GetCanPromptAudio()},
	}
	for _, probe := range probes {
		data, err := base64.StdEncoding.DecodeString(probe.data)
		if err != nil {
			return err
		}
		media := &model.PromptMedia{MimeType: probe.mime, Data: data}
		part := &model.PromptPart{}
		if probe.kind == "image" {
			part.Content = &model.PromptPart_Image{Image: media}
		} else {
			part.Content = &model.PromptPart_Audio{Audio: media}
		}
		id := "conformance-" + probe.kind
		message := "Fingerprint the attached conformance " + probe.kind + "."
		reply, err := s.client.Send(s.ctx, &model.SendRequest{Session: session.Ref, OperationId: id, Parts: []*model.PromptPart{
			{Content: &model.PromptPart_Text{Text: message}}, part,
		}})
		if !probe.supported {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			receipt := reply.GetReceipt()
			if err == nil && (receipt.GetOperationId() != id || receipt.GetStatus() != model.DeliveryStatus_DELIVERY_STATUS_UNSUPPORTED) {
				return fmt.Errorf("adapter accepted an unadvertised %s prompt", probe.kind)
			}
			continue
		}
		if err := accepted(reply.GetReceipt(), id, err); err != nil {
			return fmt.Errorf("advertised %s prompt failed: %w", probe.kind, err)
		}
		want := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		cursor, err = s.mediaReply(session.Ref, id, cursor, want)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s check) mediaReply(ref *model.SessionRef, id, cursor, want string) (string, error) {
	p := &pending{ref: ref, id: id, seen: make(map[string]bool)}
	var reply strings.Builder
	for item, err := range s.client.Observe(s.ctx, &model.ObserveRequest{Session: ref, AfterCursor: cursor}) {
		if err != nil {
			return "", fmt.Errorf("observe %s: %w", id, err)
		}
		if err := p.observe(item); err != nil {
			return "", err
		}
		if text := item.GetText(); text.GetSpeaker() == model.Speaker_SPEAKER_ASSISTANT {
			if reply.Len()+len(text.Text) > len(want) {
				return "", fmt.Errorf("%s: expected the fixture to return the SHA-256 digest of the received media", id)
			}
			reply.WriteString(text.Text)
		}
		if item.GetInput() != nil {
			return "", fmt.Errorf("%s unexpectedly requested input", id)
		}
		if end := item.GetTurnEnded(); end != nil {
			if end.Reason != "end_turn" || reply.String() != want {
				return "", fmt.Errorf("%s: fixture did not confirm the exact media bytes delivered to it", id)
			}
			return p.cursor, nil
		}
	}
	return "", fmt.Errorf("%s ended without a completed media turn", id)
}
