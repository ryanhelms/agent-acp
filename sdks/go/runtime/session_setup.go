package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	model "agent-acp.local/sdks/go/model"
	acp "github.com/coder/acp-go-sdk"
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
		if update.params.id == s.info.Ref.SessionId {
			s.publishUpdate(update.params, update.raw, update.wire)
		}
	}
	return nil
}

func selectModel(ctx context.Context, p *peer, sessionID, modelID string, initial []json.RawMessage) error {
	option, err := modelOption(initial, "")
	if err != nil {
		return err
	}
	if option == nil {
		_, err = rpc[empty](ctx, p, "session/set_model", map[string]string{"sessionId": sessionID, "modelId": modelID})
		return err
	}
	wanted := acp.SessionConfigValueId(modelID)
	if !offersModel(option.Options, wanted) {
		return fmt.Errorf("model %q was not offered by configuration option %q", modelID, option.Id)
	}
	if option.CurrentValue == wanted {
		return nil
	}
	response, err := rpc[struct{ ConfigOptions []json.RawMessage }](ctx, p, "session/set_config_option", map[string]string{
		"sessionId": sessionID, "configId": string(option.Id), "value": modelID,
	})
	if err != nil {
		return err
	}
	current, err := modelOption(response.ConfigOptions, option.Id)
	if err != nil {
		return err
	}
	if current == nil || current.CurrentValue != wanted || !offersModel(current.Options, wanted) {
		return fmt.Errorf("adapter did not confirm model %q in configuration option %q", modelID, option.Id)
	}
	return nil
}

func modelOption(options []json.RawMessage, selectedID acp.SessionConfigId) (*acp.SessionConfigOptionSelect, error) {
	for _, raw := range options {
		var header struct {
			ID       acp.SessionConfigId
			Category acp.SessionConfigOptionCategory
			Type     string
		}
		if json.Unmarshal(raw, &header) != nil || header.ID == "" || header.Type != "select" {
			continue
		}
		if selectedID == "" && header.Category != acp.SessionConfigOptionCategoryModel || selectedID != "" && selectedID != header.ID {
			continue
		}
		var option acp.SessionConfigOptionSelect
		if err := json.Unmarshal(raw, &option); err != nil {
			return nil, fmt.Errorf("invalid model configuration option %q: %w", header.ID, err)
		}
		return &option, nil
	}
	return nil, nil
}

func offersModel(options acp.SessionConfigSelectOptions, wanted acp.SessionConfigValueId) bool {
	if options.Ungrouped != nil {
		for _, option := range *options.Ungrouped {
			if option.Value == wanted {
				return true
			}
		}
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			for _, option := range group.Options {
				if option.Value == wanted {
					return true
				}
			}
		}
	}
	return false
}
