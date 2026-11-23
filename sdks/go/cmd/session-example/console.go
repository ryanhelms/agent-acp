package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type command struct {
	Op      string `json:"op"`
	ID      string `json:"id"`
	Text    string `json:"text"`
	Request string `json:"request"`
	Option  string `json:"option"`
	Turn    string `json:"turn"`
}

type record struct {
	Type      string          `json:"type"`
	Operation string          `json:"operation,omitempty"`
	ID        string          `json:"id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func emit(output *json.Encoder, kind, operation string, payload proto.Message) error {
	data, err := protojson.Marshal(payload)
	if err != nil {
		return err
	}
	return output.Encode(record{Type: kind, Operation: operation, Data: data})
}

func readHistory(ctx context.Context, client *agentacp.Sessions, ref *model.SessionRef, output *json.Encoder) error {
	req := &model.ReadHistoryRequest{Session: ref, Limit: 100}
	seen := map[string]bool{}
	for {
		page, err := client.ReadHistory(ctx, req)
		if err != nil {
			return fmt.Errorf("reading native history: %w", err)
		}
		for _, event := range page.GetObservations() {
			if err := emit(output, "history", "", event); err != nil {
				return err
			}
		}
		req.Cursor = page.GetNextCursor()
		if req.Cursor == "" {
			return nil
		}
		if seen[req.Cursor] {
			return errors.New("history pagination repeated a cursor")
		}
		seen[req.Cursor] = true
	}
}

type commandLine struct {
	command command
	err     error
}

func readCommands(ctx context.Context, input io.Reader) <-chan commandLine {
	lines := make(chan commandLine)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			var cmd command
			err := json.Unmarshal(scanner.Bytes(), &cmd)
			select {
			case lines <- commandLine{cmd, err}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- commandLine{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return lines
}

type observed struct {
	event *model.Observation
	err   error
}

func subscribe(ctx context.Context, client *agentacp.Sessions, ref *model.SessionRef) <-chan observed {
	events := make(chan observed)
	go func() {
		defer close(events)
		for event, err := range client.Observe(ctx, &model.ObserveRequest{Session: ref}) {
			select {
			case events <- observed{event, err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events
}

func execute(ctx context.Context, client *agentacp.Sessions, ref *model.SessionRef, cmd command) (*model.Receipt, error) {
	if cmd.ID == "" {
		return nil, errors.New("supply a unique operation id")
	}
	switch cmd.Op {
	case "send":
		if cmd.Text == "" {
			return nil, errors.New("send requires text")
		}
		response, err := client.Send(ctx, &model.SendRequest{Session: ref, OperationId: cmd.ID, Text: cmd.Text})
		return response.GetReceipt(), err
	case "answer":
		if cmd.Request == "" || (cmd.Option == "") == (cmd.Text == "") {
			return nil, errors.New("answer requires the request ID and exactly one of option or text")
		}
		request := &model.AnswerRequest{Session: ref, OperationId: cmd.ID, RequestId: cmd.Request}
		if cmd.Option != "" {
			request.Answer = &model.AnswerRequest_OptionId{OptionId: cmd.Option}
		} else {
			request.Answer = &model.AnswerRequest_Text{Text: cmd.Text}
		}
		response, err := client.Answer(ctx, request)
		return response.GetReceipt(), err
	case "interrupt":
		if cmd.Turn == "" {
			return nil, errors.New("interrupt requires the target turn ID")
		}
		response, err := client.Interrupt(ctx, &model.InterruptRequest{Session: ref, OperationId: cmd.ID, TurnId: cmd.Turn})
		return response.GetReceipt(), err
	default:
		return nil, fmt.Errorf("unknown operation %q", cmd.Op)
	}
}

func interact(ctx context.Context, client *agentacp.Sessions, ref *model.SessionRef, input io.Reader, output *json.Encoder, prompt []byte) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	events := subscribe(ctx, client, ref)
	commands := readCommands(ctx, input)
	activeTurn := ""
	ended := map[string]bool{}
	var batchError error
	pending := map[string]string{} // request ID -> turn ID
	apply := func(cmd command) error {
		receipt, err := execute(ctx, client, ref, cmd)
		if err != nil {
			return err
		}
		if receipt == nil {
			return errors.New("harness returned an empty operation receipt")
		}
		if err := emit(output, "receipt", cmd.Op, receipt); err != nil {
			return err
		}
		switch receipt.GetStatus() {
		case model.DeliveryStatus_DELIVERY_STATUS_ACCEPTED, model.DeliveryStatus_DELIVERY_STATUS_DELIVERED:
			switch cmd.Op {
			case "send":
				if !ended[cmd.ID] {
					activeTurn = cmd.ID
				}
			case "answer":
				delete(pending, cmd.Request)
			}
		default:
			return fmt.Errorf("%s: %s", receipt.GetStatus(), receipt.GetDetail())
		}
		return nil
	}
	if len(prompt) != 0 {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		if err := apply(command{Op: "send", ID: fmt.Sprintf("prompt-%x", id), Text: string(prompt)}); err != nil {
			return err
		}
	}
	for {
		if commands == nil {
			if len(pending) != 0 {
				return errors.New("input closed while the harness awaited a decision")
			}
			if activeTurn == "" {
				return batchError
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, open := <-commands:
			if !open {
				commands = nil
				continue
			}
			if line.err == nil && line.command.Op == "quit" {
				return nil
			}
			err := line.err
			if err == nil {
				err = apply(line.command)
			}
			if err != nil {
				batchError = err
				if writeErr := output.Encode(record{Type: "error", Operation: line.command.Op, ID: line.command.ID, Error: err.Error()}); writeErr != nil {
					return writeErr
				}
			}
		case update, open := <-events:
			if !open {
				return errors.New("live observation stream closed")
			}
			if update.err != nil {
				return update.err
			}
			event := update.event
			if err := emit(output, "observation", "", event); err != nil {
				return err
			}
			if request := event.GetInput(); request != nil {
				pending[request.GetRequestId()] = event.GetTurnId()
			}
			if terminal := event.GetTurnEnded(); terminal != nil {
				ended[event.GetTurnId()] = true
				switch terminal.GetReason() {
				case "end_turn", "cancelled", "max_tokens", "max_turn_requests", "refusal":
				default:
					batchError = fmt.Errorf("turn ended: %s", terminal.GetReason())
				}
				if activeTurn == event.GetTurnId() {
					activeTurn = ""
				}
				for id, turn := range pending {
					if turn == event.GetTurnId() {
						delete(pending, id)
					}
				}
			}
		}
	}
}
