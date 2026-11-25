package nativehistory

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	model "agent-acp.local/sdks/go/model"
)

//go:embed claude.mjs
var claudeReader string

func claudeBatch(ctx context.Context, cwd, id, cursor string) ([]*model.Observation, string, error) {
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", errors.New("invalid Claude history offset")
		}
		offset = n
	}
	var anchor string
	if dir := os.Getenv("AGENT_ACP_HARNESS_DIR"); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, "", err
		}
		anchor = filepath.Join(abs, "package.json")
	} else {
		bin, err := exec.LookPath("claude-agent-acp")
		if err != nil {
			return nil, "", errors.New("set AGENT_ACP_HARNESS_DIR to the installed npm harness directory or add claude-agent-acp to PATH")
		}
		anchor, err = filepath.EvalSymlinks(bin)
		if err != nil {
			return nil, "", err
		}
	}
	const pageSize = 24
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "-e", claudeReader, "--", anchor, id, cwd, strconv.Itoa(offset), strconv.Itoa(pageSize+1))
	out, err := cmd.Output()
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			return nil, "", cause
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, "", fmt.Errorf("reading Claude history: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, "", fmt.Errorf("starting Claude history reader: %w", err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(out, &messages); err != nil {
		return nil, "", fmt.Errorf("decoding Claude history: %w", err)
	}
	next := ""
	if len(messages) > pageSize {
		messages = messages[:pageSize]
		next = strconv.Itoa(offset + pageSize)
	}
	var events []*model.Observation
	for _, raw := range messages {
		mapped, err := claudeMessage(raw)
		if err != nil {
			return nil, "", err
		}
		events = append(events, withSource("claude.history.message", raw, mapped)...)
	}
	return events, next, nil
}

func claudeMessage(raw json.RawMessage) ([]*model.Observation, error) {
	var message struct {
		Type    string
		Message json.RawMessage
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, err
	}
	speaker := map[string]model.Speaker{"user": model.Speaker_SPEAKER_USER, "assistant": model.Speaker_SPEAKER_ASSISTANT, "system": model.Speaker_SPEAKER_SYSTEM}[message.Type]
	var body struct{ Content json.RawMessage }
	// System record bodies and custom provider payloads may not be chat messages.
	// Keep unrecognized originals in a passive extension.
	_ = json.Unmarshal(message.Message, &body)
	content := body.Content
	if len(content) == 0 || speaker == model.Speaker_SPEAKER_UNSPECIFIED {
		return []*model.Observation{extension("claude.message", raw)}, nil
	}
	var plain string
	if content[0] == '"' && json.Unmarshal(content, &plain) == nil {
		return []*model.Observation{text(speaker, plain)}, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
		return []*model.Observation{extension("claude.message", raw)}, nil
	}
	var events []*model.Observation
	for _, block := range blocks {
		var b struct {
			Type, Text, ID, Name string
			ToolUseID            string `json:"tool_use_id"`
			IsError              bool   `json:"is_error"`
			Input, Content       json.RawMessage
		}
		if err := json.Unmarshal(block, &b); err != nil {
			events = append(events, extension("claude.content", block))
			continue
		}
		switch b.Type {
		case "text":
			events = append(events, text(speaker, b.Text))
		case "tool_use":
			events = append(events, tool(b.ID, b.Name, "started", string(b.Input)))
		case "tool_result":
			phase := "completed"
			if b.IsError {
				phase = "failed"
			}
			summary := string(b.Content)
			if len(b.Content) > 0 && b.Content[0] == '"' {
				_ = json.Unmarshal(b.Content, &summary)
			}
			events = append(events, tool(b.ToolUseID, "", phase, summary))
		default:
			events = append(events, extension("claude."+b.Type, block))
		}
	}
	return events, nil
}
