package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/proto"
)

type setupUpdate struct {
	params updateParams
	raw    json.RawMessage
	wire   *model.WireProvenance
}

type sessionSetup struct {
	updates  []setupUpdate
	bytes    int
	overflow bool
}

func (s *session) beginSetup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup = &sessionSetup{}
	s.wireSetup.Store(true)
}

func (q *sessionSetup) add(params updateParams, raw json.RawMessage, wire *model.WireProvenance) {
	size := len(raw) + proto.Size(wire)
	if len(q.updates) >= liveWindow || size > liveWindowBytes-q.bytes {
		q.overflow = true
	}
	if q.overflow {
		return
	}
	q.updates = append(q.updates, setupUpdate{params, raw, wire})
	q.bytes += size
}

func (s *session) finishSetup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	setup := s.setup
	s.setup = nil
	s.wireSetup.Store(false)
	if setup.overflow {
		return errors.New("adapter exceeded the session setup update budget")
	}
	for _, update := range setup.updates {
		s.routeUpdate(update.params, update.raw, update.wire)
	}
	return nil
}

func selectModel(ctx context.Context, p *peer, sessionID, modelID string, initial []json.RawMessage) error {
	option, err := modelOption(initial, "", p.version)
	if err != nil {
		return err
	}
	if option == nil {
		if p.version == 2 {
			return fmt.Errorf("adapter did not advertise a configuration option for model %q", modelID)
		}
		_, err = rpc[empty](ctx, p, "session/set_model", map[string]string{"sessionId": sessionID, "modelId": modelID})
		return err
	}
	wanted := modelID
	if !option.offers(wanted) {
		return fmt.Errorf("model %q was not offered by configuration option %q", modelID, option.ID)
	}
	if option.CurrentValue == wanted {
		return nil
	}
	params := map[string]string{"sessionId": sessionID, "configId": string(option.ID), "value": modelID}
	if p.version == 2 {
		params["type"] = "id"
	}
	response, err := rpc[struct{ ConfigOptions []json.RawMessage }](ctx, p, "session/set_config_option", params)
	if err != nil {
		return err
	}
	current, err := modelOption(response.ConfigOptions, option.ID, p.version)
	if err != nil {
		return err
	}
	if current == nil || current.CurrentValue != wanted || !current.offers(wanted) {
		return fmt.Errorf("adapter did not confirm model %q in configuration option %q", modelID, option.ID)
	}
	return nil
}

type modelConfigOption struct {
	ID           string
	CurrentValue string
	Options      []struct {
		Value   string
		Options []struct{ Value string }
	}
}

func modelOption(options []json.RawMessage, selectedID string, version int) (*modelConfigOption, error) {
	for _, raw := range options {
		var header struct{ ID, ConfigID, Category, Type string }
		if json.Unmarshal(raw, &header) != nil || header.Type != "select" {
			continue
		}
		id := header.ID
		if version == 2 {
			id = header.ConfigID
		}
		if id == "" || selectedID == "" && header.Category != "model" || selectedID != "" && selectedID != id {
			continue
		}
		var option modelConfigOption
		if err := json.Unmarshal(raw, &option); err != nil {
			return nil, fmt.Errorf("invalid model configuration option %q: %w", id, err)
		}
		option.ID = id
		return &option, nil
	}
	return nil, nil
}

func (option modelConfigOption) offers(wanted string) bool {
	for _, choice := range option.Options {
		if choice.Value == wanted {
			return true
		}
		for _, grouped := range choice.Options {
			if grouped.Value == wanted {
				return true
			}
		}
	}
	return false
}
