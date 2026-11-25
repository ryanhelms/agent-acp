package runtime

import (
	"encoding/json"

	model "agent-acp.local/sdks/go/model"
	acp "github.com/coder/acp-go-sdk"
)

func sessionChange(raw json.RawMessage) *model.SessionUpdate {
	var update acp.SessionUpdate
	if json.Unmarshal(raw, &update) != nil {
		return nil
	}
	switch {
	case update.Plan != nil:
		plan := &model.PlanSnapshot{}
		for _, entry := range update.Plan.Entries {
			plan.Entries = append(plan.Entries, &model.PlanSnapshot_Entry{
				Content: entry.Content, Priority: string(entry.Priority), Status: string(entry.Status),
			})
		}
		return &model.SessionUpdate{Change: &model.SessionUpdate_Plan{Plan: plan}}
	case update.AvailableCommandsUpdate != nil:
		commands := &model.AvailableCommands{}
		for _, command := range update.AvailableCommandsUpdate.AvailableCommands {
			item := &model.AvailableCommands_Command{Name: command.Name, Description: command.Description}
			if command.Input != nil && command.Input.Unstructured != nil {
				item.InputHint = &command.Input.Unstructured.Hint
			}
			commands.Commands = append(commands.Commands, item)
		}
		return &model.SessionUpdate{Change: &model.SessionUpdate_Commands{Commands: commands}}
	case update.CurrentModeUpdate != nil:
		mode := &model.ModeChanged{ModeId: string(update.CurrentModeUpdate.CurrentModeId)}
		return &model.SessionUpdate{Change: &model.SessionUpdate_Mode{Mode: mode}}
	case update.ConfigOptionUpdate != nil:
		return &model.SessionUpdate{Change: &model.SessionUpdate_Configuration{
			Configuration: configSnapshot(update.ConfigOptionUpdate.ConfigOptions),
		}}
	case update.SessionInfoUpdate != nil:
		info := update.SessionInfoUpdate
		return &model.SessionUpdate{Change: &model.SessionUpdate_Info{Info: &model.SessionInfoChanged{
			Title: info.Title.Ptr(), UpdatedAt: info.UpdatedAt.Ptr(),
			ClearTitle: info.Title.IsNull(), ClearUpdatedAt: info.UpdatedAt.IsNull(),
		}}}
	case update.UsageUpdate != nil:
		value := update.UsageUpdate
		usage := &model.UsageChanged{Used: value.Used, Size: value.Size}
		if value.Cost != nil {
			usage.Cost = &model.UsageChanged_Cost{Amount: value.Cost.Amount, Currency: value.Cost.Currency}
		}
		return &model.SessionUpdate{Change: &model.SessionUpdate_Usage{Usage: usage}}
	default:
		return nil
	}
}

func configSnapshot(options []acp.SessionConfigOption) *model.ConfigurationSnapshot {
	result := &model.ConfigurationSnapshot{}
	for _, option := range options {
		var item *model.ConfigurationSnapshot_Option
		switch {
		case option.Select != nil:
			selected := option.Select
			item = &model.ConfigurationSnapshot_Option{
				Id: string(selected.Id), Name: selected.Name, Kind: "select",
				Description: optionalString(selected.Description), Category: optionalString(selected.Category),
				CurrentValue: &model.ConfigurationSnapshot_Option_Selected{Selected: string(selected.CurrentValue)},
			}
			if options := selected.Options.Ungrouped; options != nil {
				item.Choices = configChoices(*options)
			}
			if groups := selected.Options.Grouped; groups != nil {
				for _, group := range *groups {
					item.Groups = append(item.Groups, &model.ConfigurationSnapshot_Group{
						Id: string(group.Group), Name: group.Name, Choices: configChoices(group.Options),
					})
				}
			}
		case option.Boolean != nil:
			toggle := option.Boolean
			item = &model.ConfigurationSnapshot_Option{
				Id: string(toggle.Id), Name: toggle.Name, Kind: "boolean",
				Description: optionalString(toggle.Description), Category: optionalString(toggle.Category),
				CurrentValue: &model.ConfigurationSnapshot_Option_Enabled{Enabled: toggle.CurrentValue},
			}
		}
		if item != nil {
			result.Options = append(result.Options, item)
		}
	}
	return result
}

func configChoices(choices []acp.SessionConfigSelectOption) []*model.ConfigurationSnapshot_Choice {
	result := make([]*model.ConfigurationSnapshot_Choice, 0, len(choices))
	for _, choice := range choices {
		result = append(result, &model.ConfigurationSnapshot_Choice{
			Value: string(choice.Value), Name: choice.Name, Description: optionalString(choice.Description),
		})
	}
	return result
}

func optionalString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func messageContent(raw json.RawMessage, speaker model.Speaker, thought bool) *model.Observation {
	var block struct {
		Type, Text, URI, MimeType, Name, Title, Description string
		Size                                                *int64
		Resource                                            struct{ URI, MimeType, Text string }
	}
	if json.Unmarshal(raw, &block) != nil || block.Type == "" {
		return nil
	}
	switch block.Type {
	case "text":
		if !thought {
			return &model.Observation{Event: &model.Observation_Text{Text: &model.TextUpdate{Speaker: speaker, Text: block.Text}}}
		}
	case "image", "audio", "resource_link":
	case "resource":
		block.URI, block.MimeType, block.Text = block.Resource.URI, block.Resource.MimeType, block.Resource.Text
	default:
		return nil
	}
	item := &model.MessageContent{
		Speaker: speaker, Thought: thought, Kind: block.Type, Text: boundedToolText(block.Text),
		Uri: block.URI, MimeType: block.MimeType, Name: block.Name, Title: block.Title,
		Description: block.Description, Size: block.Size,
	}
	return &model.Observation{Event: &model.Observation_Content{Content: item}}
}
