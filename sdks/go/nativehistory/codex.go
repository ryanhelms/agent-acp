package nativehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	model "agent-acp.local/sdks/go/model"
)

// The app server's thread/read and thread/turns/list are passive. In particular,
// session/load on the ACP adapter would call thread/resume and activate the thread.
func codexBatch(ctx context.Context, _ string, id, cursor string) ([]*model.Observation, string, error) {
	bin := os.Getenv("CODEX_PATH")
	if bin == "" {
		bin = "codex"
	}
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("starting Codex app server: %w", err)
	}
	defer func() {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	encoder, decoder := json.NewEncoder(in), json.NewDecoder(out)
	seq := 0
	call := func(method string, params any, target any) error {
		seq++
		if err := encoder.Encode(map[string]any{"id": seq, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			var reply struct {
				ID     json.RawMessage
				Method string
				Result json.RawMessage
				Error  *struct{ Message string }
			}
			if err := decoder.Decode(&reply); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("reading Codex %s response: %w", method, err)
			}
			if len(reply.ID) == 0 {
				continue
			} // unsolicited notification
			if reply.Method != "" || string(reply.ID) != strconv.Itoa(seq) {
				return fmt.Errorf("unexpected Codex message while reading %s", method)
			}
			if reply.Error != nil {
				return fmt.Errorf("Codex %s: %s", method, reply.Error.Message)
			}
			if target == nil {
				return nil
			}
			return json.Unmarshal(reply.Result, target)
		}
	}
	if err := call("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "agent-acp-history", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": false, "requestAttestation": false},
	}, nil); err != nil {
		return nil, "", err
	}
	if err := encoder.Encode(map[string]string{"method": "initialized"}); err != nil {
		return nil, "", err
	}
	var metadata struct{ Thread struct{ ID string } }
	if err := call("thread/read", map[string]any{"threadId": id, "includeTurns": false}, &metadata); err != nil {
		return nil, "", err
	}
	if metadata.Thread.ID != id {
		return nil, "", errors.New("Codex returned history metadata for a different thread")
	}
	params := map[string]any{"threadId": id, "limit": 8, "sortDirection": "asc", "itemsView": "full"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var page struct {
		Data []struct {
			ID, Status, ItemsView string
			Items                 []json.RawMessage
		}
		NextCursor string
	}
	if err := call("thread/turns/list", params, &page); err != nil {
		return nil, "", err
	}
	var events []*model.Observation
	for _, turn := range page.Data {
		if turn.ItemsView != "full" {
			return nil, "", errors.New("Codex returned incomplete turn items")
		}
		start := len(events)
		for _, raw := range turn.Items {
			mapped, err := codexItem(raw)
			if err != nil {
				return nil, "", err
			}
			events = append(events, mapped...)
		}
		if turn.Status != "" && turn.Status != "inProgress" {
			events = append(events, &model.Observation{Event: &model.Observation_TurnEnded{TurnEnded: &model.TurnEnded{Reason: turn.Status}}})
		}
		for _, event := range events[start:] {
			event.TurnId = turn.ID
		}
	}
	return events, page.NextCursor, nil
}

func codexItem(raw json.RawMessage) ([]*model.Observation, error) {
	var item struct {
		Type, ID, Text, Tool, Name, Status, Command, AggregatedOutput string
		Content                                                       json.RawMessage
		Questions                                                     []json.RawMessage
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	var event *model.Observation
	switch item.Type {
	case "userMessage":
		var parts []json.RawMessage
		if err := json.Unmarshal(item.Content, &parts); err != nil {
			return nil, err
		}
		var events []*model.Observation
		for _, part := range parts {
			var block struct{ Type, Text string }
			if err := json.Unmarshal(part, &block); err != nil {
				return nil, err
			}
			if block.Type == "text" {
				events = append(events, text(model.Speaker_SPEAKER_USER, block.Text))
			} else {
				events = append(events, extension("codex.user."+block.Type, part))
			}
		}
		return events, nil
	case "agentMessage", "plan":
		event = text(model.Speaker_SPEAKER_ASSISTANT, item.Text)
		if len(item.Questions) > 0 {
			// Historical questions retain their native choices but cannot be answered.
			return []*model.Observation{event, extension("codex.agentMessage.questions", raw)}, nil
		}
	case "commandExecution":
		summary := item.Command
		if item.AggregatedOutput != "" {
			summary += "\n" + item.AggregatedOutput
		}
		event = tool(item.ID, "commandExecution", item.Status, summary)
	case "fileChange", "dynamicToolCall", "mcpToolCall", "collabAgentToolCall", "functionCallOutput":
		name := item.Tool
		if name == "" {
			name = item.Name
		}
		if name == "" {
			name = item.Type
		}
		event = tool(item.ID, name, item.Status, string(raw))
	default:
		event = extension("codex."+item.Type, raw)
	}
	return []*model.Observation{event}, nil
}
