package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	model "agent-acp.local/sdks/go/model"
)

const (
	// ACPChildUpdateSource carries exact method params for a verified child
	// on its root session's scoped observation stream.
	ACPChildUpdateSource = "acp.child_update"
	// ACPChildPermissionSource carries the original passive evidence of an
	// addressed child permission that the runtime declined.
	ACPChildPermissionSource = "acp.child_permission"
)

const maxChildren = 256

type ChildSession struct {
	ID, ParentID       string
	Title, Description string
	State, StopReason  string
	CanCancel          bool
}

type childSessions map[string]ChildSession

// announce applies only a verified association. A session can never be moved
// beneath a different parent or substitute itself for a known ancestor.
func (children childSessions) announce(root, parent string, update json.RawMessage) bool {
	if parent != root {
		if _, known := children[parent]; !known {
			return false
		}
	}
	var patch struct {
		SessionID           string
		Title, Description  json.RawMessage
		Capabilities, State json.RawMessage
	}
	if json.Unmarshal(update, &patch) != nil || patch.SessionID == "" || len(patch.SessionID) > 4096 ||
		patch.SessionID == root || patch.SessionID == parent {
		return false
	}
	child, known := children[patch.SessionID]
	if known && child.ParentID != parent || !known && len(children) >= maxChildren {
		return false
	}
	child.ID, child.ParentID = patch.SessionID, parent
	patchV2String(&child.Title, patch.Title)
	patchV2String(&child.Description, patch.Description)
	if len(patch.Capabilities) != 0 {
		var advertised struct{ Cancel *empty }
		child.CanCancel = json.Unmarshal(patch.Capabilities, &advertised) == nil && advertised.Cancel != nil
	}
	if len(patch.State) != 0 {
		child.replaceState(patch.State)
	}
	children[child.ID] = child
	return true
}

func (child *ChildSession) replaceState(raw json.RawMessage) {
	var activity struct{ State, StopReason json.RawMessage }
	if json.Unmarshal(raw, &activity) == nil {
		child.State, child.StopReason = "", ""
		patchV2String(&child.State, activity.State)
		patchV2String(&child.StopReason, activity.StopReason)
	}
}

func updateWithKind(update json.RawMessage, kind string) bool {
	if !bytes.Contains(update, []byte(kind)) && !bytes.Contains(update, []byte{'\\'}) {
		return false
	}
	var header struct{ SessionUpdate string }
	return json.Unmarshal(update, &header) == nil && header.SessionUpdate == kind
}

func childAnnouncement(update json.RawMessage) bool { return updateWithKind(update, "subagent_update") }

func noticeUpdate(update json.RawMessage) bool { return updateWithKind(update, "notice") }

func (s *session) routeUpdate(msg updateParams, raw json.RawMessage, wire *model.WireProvenance) {
	root := s.info.Ref.SessionId
	if s.children != nil && childAnnouncement(msg.update) && !s.children.announce(root, msg.id, msg.update) {
		return
	}
	if msg.id == root {
		s.publishUpdate(msg, raw, wire)
		return
	}
	if child, known := s.children[msg.id]; known {
		if s.v2 != nil && updateWithKind(msg.update, "state_update") {
			child.replaceState(msg.update)
			s.children[msg.id] = child
		}
		// Keep the child's payload in the parent's authorized feed without
		// attributing its tool IDs, approvals, or stop reasons to the parent.
		s.emitWireSource(nil, ACPChildUpdateSource, raw, wire)
	}
}

// ChildSessions returns the most recently reported roster under the bound root.
func (r *Runtime) ChildSessions(root *model.SessionRef) ([]ChildSession, error) {
	s, err := r.session(root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	roster := make([]ChildSession, 0, len(s.children))
	for _, child := range s.children {
		if s.closed || s.p.disconnected() {
			child.State, child.StopReason, child.CanCancel = "unknown", "", false
		}
		roster = append(roster, child)
	}
	sort.Slice(roster, func(i, j int) bool { return roster[i].ID < roster[j].ID })
	return roster, nil
}

// CancelChild sends the only client-initiated operation permitted for a child.
// The opaque ID is resolved within the selected root, using its current
// capability. A successful write is not evidence the child stopped.
func (r *Runtime) CancelChild(ctx context.Context, root *model.SessionRef, childID string) error {
	if !r.config.ChildSessions {
		return errors.New("child cancellation is not enabled")
	}
	s, err := r.session(root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if child, known := s.children[childID]; !known || !child.CanCancel || s.closed || s.p.disconnected() {
		return errors.New("child has no current cancel capability for this parent")
	}
	return s.p.conn.SendNotification(ctx, "session/cancel", map[string]string{"sessionId": childID})
}

func (r *Runtime) knownChild(id string) bool {
	if !r.config.ChildSessions && r.config.ProtocolVersion != 2 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		s.mu.Lock()
		_, classified := s.children[id]
		s.mu.Unlock()
		if classified {
			return true
		}
	}
	return false
}
