package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	agentacp "agent-acp.local/sdks/go"
	model "agent-acp.local/sdks/go/model"
	"agent-acp.local/sdks/go/nativehistory"
	"agent-acp.local/sdks/go/runtime"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("session-example", flag.ContinueOnError)
	provider := flags.String("provider", "", "installed profile: claude or codex (provides default adapter and history reader)")
	instance := flags.String("instance", "", "stable name of this configured harness")
	cwd := flags.String("cwd", ".", "working directory for the agent")
	modelName := flags.String("model", "", "optional model for a new session")
	resumeID := flags.String("resume", "", "resume this native session")
	historyID := flags.String("history", "", "read retained history for this native session and exit")
	promptFile := flags.String("prompt-file", "", "send the exact text in this file, or - for standard input")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: session-example -instance NAME [-provider claude|codex] [flags] [-- adapter [args...]]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "Enter one JSON command per line. Example: "+`{"op":"send","id":"ask-1","text":"Hello"}`)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *instance == "" || (*provider == "" && flags.NArg() < 1) {
		flags.Usage()
		return errors.New("supply an instance name and either a provider profile or an adapter executable")
	}
	var adapter string
	switch *provider {
	case "claude":
		adapter = "claude-agent-acp"
	case "codex":
		adapter = "codex-acp"
	case "":
		if *historyID != "" {
			return errors.New("-history requires a provider profile")
		}
	default:
		return fmt.Errorf("unknown provider profile %q", *provider)
	}
	var adapterArgs []string
	if flags.NArg() > 0 {
		adapter, adapterArgs = flags.Arg(0), flags.Args()[1:]
	}
	if *historyID != "" && (*resumeID != "" || *promptFile != "" || *modelName != "") {
		return errors.New("-history is a standalone read operation")
	}
	if *resumeID != "" && *modelName != "" {
		return errors.New("-model selects the model for a new session")
	}
	workingDir, err := filepath.Abs(*cwd)
	if err != nil {
		return err
	}
	var prompt []byte
	if *promptFile != "" {
		if *promptFile == "-" {
			prompt, err = io.ReadAll(stdin)
		} else {
			prompt, err = os.ReadFile(*promptFile)
		}
		if err != nil {
			return fmt.Errorf("reading prompt: %w", err)
		}
		if len(prompt) == 0 {
			return errors.New("prompt file is empty")
		}
	}

	rt, err := runtime.New(runtime.Config{
		HarnessInstance: *instance, WorkingDirectory: workingDir,
		Command: adapter, Args: adapterArgs,
		ReadHistory: nativehistory.ForHarness(*provider, workingDir),
	})
	if err != nil {
		return err
	}
	defer rt.Close()

	client := agentacp.New(rt)
	output := json.NewEncoder(stdout)
	ref := &model.SessionRef{HarnessInstance: *instance}
	if *historyID != "" {
		ref.SessionId = *historyID
		return readHistory(ctx, client, ref, output)
	}
	var session *model.Session
	if *resumeID != "" {
		ref.SessionId = *resumeID
		response, err := client.Resume(ctx, &model.ResumeRequest{Session: ref})
		if err != nil {
			return fmt.Errorf("resuming session: %w", err)
		}
		session = response.GetSession()
	} else {
		response, err := client.Open(ctx, &model.OpenRequest{
			HarnessInstance: *instance, WorkingDirectory: workingDir, Model: *modelName,
		})
		if err != nil {
			return fmt.Errorf("opening session: %w", err)
		}
		session = response.GetSession()
	}
	if session.GetRef().GetSessionId() == "" {
		return errors.New("harness returned an empty session ID")
	}
	if err := emit(output, "session", "", session); err != nil {
		return err
	}
	return interact(ctx, client, session.GetRef(), stdin, output, prompt)
}
