package runtime

import (
	"bytes"
	"encoding/json"

	model "agent-acp.local/sdks/go/model"
)

// ACPCompactionStateSource carries a bounded compaction snapshot. Its separate
// source record is the exact update or summary chunk received from the agent.
const ACPCompactionStateSource = "acp.compaction_state"

type compactionState struct {
	v2    bool
	items map[string]*compaction
	order []string
}

type compaction struct {
	ID      string                     `json:"compactionId"`
	Status  string                     `json:"status"`
	Error   string                     `json:"error,omitempty"`
	First   uint64                     `json:"firstOrdinal"`
	Summary []json.RawMessage          `json:"summary"`
	Meta    map[string]json.RawMessage `json:"_meta,omitempty"`
	Omitted bool                       `json:"omitted,omitempty"`
	bytes   int
}

func (state *compactionState) observe(raw json.RawMessage, ordinal uint64) *model.Observation {
	if !bytes.Contains(raw, []byte("compaction")) && !bytes.Contains(raw, []byte{'\\'}) {
		return nil
	}
	var update struct {
		SessionUpdate, CompactionID     string
		Status, Summary, Content, Error json.RawMessage
		Meta                            json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &update) != nil || update.CompactionID == "" || len(update.CompactionID) > 4096 {
		return nil
	}
	chunk := update.SessionUpdate == "compaction_summary_chunk"
	if !chunk && update.SessionUpdate != "compaction_update" {
		return nil
	}
	if chunk && (len(update.Content) == 0 || string(update.Content) == "null") {
		return nil
	}
	if !chunk && !state.v2 && (len(update.Status) == 0 || string(update.Status) == "null") {
		return nil
	}
	if state.items == nil {
		state.items = make(map[string]*compaction)
	}
	item := state.items[update.CompactionID]
	if item == nil {
		if len(state.order) >= v2MessageCount {
			delete(state.items, state.order[0])
			copy(state.order, state.order[1:])
			state.order = state.order[:len(state.order)-1]
		}
		item = &compaction{ID: update.CompactionID, Status: "in_progress", First: ordinal, Summary: []json.RawMessage{}}
		state.items[item.ID] = item
		state.order = append(state.order, item.ID)
	}
	if chunk {
		if !item.Omitted {
			if item.bytes+len(update.Content) > v2MessageBytes || len(item.Summary) >= liveWindow {
				item.Summary, item.Omitted = nil, true
			} else {
				item.Summary = append(item.Summary, update.Content)
			}
		}
	} else {
		patchV2String(&item.Status, update.Status)
		if item.Status == "" {
			item.Status = "in_progress"
		}
		patchV2String(&item.Error, update.Error)
		if len(update.Summary) > 0 {
			var summary []json.RawMessage
			if len(update.Summary) > v2MessageBytes || json.Unmarshal(update.Summary, &summary) != nil || len(summary) > liveWindow {
				item.Summary, item.Omitted = nil, true
			} else {
				item.Summary, item.Omitted = summary, false
				if item.Summary == nil {
					item.Summary = []json.RawMessage{}
				}
			}
		}
		item.applyMeta(update.Meta, state.v2)
	}
	body, _ := json.Marshal(item)
	if len(body) > v2MessageBytes {
		item.Summary, item.Meta, item.Omitted = nil, nil, true
		body, _ = json.Marshal(item)
	}
	item.bytes = len(body)
	return &model.Observation{Event: &model.Observation_Extension{Extension: &model.Extension{
		Type: ACPCompactionStateSource, MediaType: "application/json", Data: body,
	}}}
}

func (item *compaction) applyMeta(raw json.RawMessage, patch bool) {
	if len(raw) == 0 {
		return
	}
	if string(raw) == "null" {
		item.Meta = nil
		return
	}
	var values map[string]json.RawMessage
	if len(raw) > v2MessageBytes || json.Unmarshal(raw, &values) != nil {
		return
	}
	if !patch {
		item.Meta = values
		return
	}
	if item.Meta == nil {
		item.Meta = make(map[string]json.RawMessage)
	}
	for name, value := range values {
		if string(value) == "null" {
			delete(item.Meta, name)
		} else {
			item.Meta[name] = value
		}
	}
}
