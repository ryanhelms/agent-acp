// adapter-conformance checks an external ACP executable against a fixture.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	agentacp "agent-acp.local/sdks/go"
	"agent-acp.local/sdks/go/conformance"
	"agent-acp.local/sdks/go/nativehistory"
	"agent-acp.local/sdks/go/runtime"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("adapter-conformance", flag.ContinueOnError)
	path := flags.String("fixture", "", "JSON scenario file")
	cwd := flags.String("cwd", "", "disposable working directory")
	instance := flags.String("instance", "conformance", "harness instance name")
	history := flags.String("history-provider", "", "optional native history adapter: claude or codex")
	timeout := flags.Duration("timeout", 60*time.Second, "total fixture timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || *cwd == "" || flags.NArg() == 0 || *timeout <= 0 {
		return errors.New("supply -fixture, -cwd, and -- ADAPTER [args...] with a positive timeout")
	}
	working, err := filepath.Abs(*cwd)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var scenario conformance.Scenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		return fmt.Errorf("decode conformance fixture: %w", err)
	}
	reader := nativehistory.ForHarness(*history, working)
	if *history != "" && reader == nil {
		return fmt.Errorf("unsupported history provider %q", *history)
	}
	config := runtime.Config{
		HarnessInstance: *instance, WorkingDirectory: working, Command: flags.Arg(0), Args: flags.Args()[1:],
		Stderr: os.Stderr, OncePermissionsOnly: true, ReadHistory: reader,
	}
	adapter, err := runtime.New(config)
	if err != nil {
		return err
	}
	defer func() { _ = adapter.Close() }()
	restart := func(ctx context.Context) (agentacp.Runtime, error) {
		if err := adapter.Close(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fresh, err := runtime.New(config)
		if err != nil {
			return nil, err
		}
		adapter = fresh
		return fresh, nil
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := conformance.CheckWithRestart(ctx, adapter, conformance.Target{HarnessInstance: *instance, WorkingDirectory: working}, scenario, restart); err != nil {
		return err
	}
	fmt.Println("Adapter conformance passed.")
	return nil
}
