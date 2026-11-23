# agent-acp

agent-acp gives applications one typed session API for coding harnesses. The Go, TypeScript and Python packages share a versioned schema and a conformance fixture. Applications can open or resume sessions, send literal text, observe a stream, answer a specific request, interrupt a turn, and query native history.

The Go package includes a live runtime for Claude Code and Codex, native history readers, and a terminal example. TypeScript and Python currently ship the generated facade, models, and runtime interface. Their conformance tests use scripted runtimes.

## Try a live session

With Go 1.23+, Node.js 22+, and an account signed in to the provider on this machine, run these commands from the repository root. The optional npm install provides the pinned adapters alongside the repository.

```sh
npm ci --prefix harnesses/local
export AGENT_ACP_HARNESS_DIR="$PWD/harnesses/local"
export PATH="$AGENT_ACP_HARNESS_DIR/node_modules/.bin:$PATH"
(cd sdks/go && go build -o /tmp/agent-session-example ./cmd/session-example)
/tmp/agent-session-example -provider claude -instance claude-local -cwd "$PWD"
```

Enter `{"op":"send","id":"hello-1","text":"Reply with hello."}` and watch the response stream. Enter `{"op":"quit"}` to close the session process. For Codex, use `-provider codex -instance codex-local`. Existing compatible adapters can also be selected through `PATH` or passed as an executable and arguments. The example guide covers permission replies, targeted interrupts, and reopening a conversation. [Live session example](sdks/go/cmd/session-example/README.md)

## Repository layout

| Path | Responsibility |
| --- | --- |
| [schema/agentacp/v1/session.proto](schema/agentacp/v1/session.proto) | Public operation names, request and response types, event variants and delivery outcomes |
| [sdks/go](sdks/go) | Go facade, live stdio runtime, native history readers and JSON Lines example |
| [sdks/typescript](sdks/typescript) | TypeScript package with promises, async iterables and abort signals |
| [sdks/python](sdks/python) | Python package with awaitable calls and async observation streams |
| [tools](tools) | Pinned model generators and templates that turn service descriptors into typed client methods |
| [conformance](conformance) | One language-neutral fixture exercised through each public client |

## Generate and check

From the repository root, install Node.js 22 or newer, npm, Go 1.23 or newer, Python 3.10 or newer, and Make on macOS or Linux. The setup command installs the development dependencies locally. Package users consume the generated sources in their language package.

```sh
make setup
make generate
make check
```

Use `npm run generate -- --language typescript` to regenerate one binding. Substitute `go` or `python` for the other targets. Use `npm run check:generated` to regenerate into a temporary directory and compare the result with the checked-in code. The shared fixture and the language tests are described in [conformance](conformance/README.md).

## Runtime boundary

The Protobuf service describes a callable API. The generated clients pass a typed request to an injected runtime and expose typed replies and observations to their callers. The runtime resolves the harness instance, negotiates capabilities, opens the provider connection and handles transport errors. ACP, a provider SDK and a native history reader can all sit behind that interface. A local adapter can execute in the consuming application's process.

Configure each Go runtime with a stable harness instance, an adapter command and arguments, and an absolute working directory. Pass it to `agentacp.New`. The adapter starts on demand. Register stdio MCP servers through `runtime.Config.MCPServers`. Set `OncePermissionsOnly` to restrict permission prompts to one-time decisions. The runnable example shows the complete call sequence. [Go example entry point](sdks/go/cmd/session-example/main.go)

| Operation | Go runtime behavior |
| --- | --- |
| Open or resume | Each live session owns an adapter process. The provider issues the session ID and advertises its ability to resume and list sessions. |
| Send and observe | One active turn per session. Literal text travels over a structured message. A send operation ID identifies the turn, and observations carry the originating session and cursor. |
| Answer or interrupt | A permission answer names the session, live request, and one of its offered choices. An interrupt targets the active turn by ID. Observe its end to confirm cancellation. |
| Reconnect an observer | Supply its last cursor. A gap signals that events fell outside the in-memory window or the live connection was replaced. Closing the observer releases the subscription. |
| Browse and read history | `ListSessions` queries the live adapter’s native listing. `ReadHistory` uses the configured passive reader. Claude reads retained messages through the Claude Agent SDK. Codex uses App Server’s `thread/read` and `thread/turns/list`. |

Native storage and retention remain under provider control. A history-only runtime can omit the live adapter command. Live delivery receipts are held in memory during the runtime’s lifetime. Applications that need operation reconciliation across process restarts own their operation IDs and receipt records.

## Package releases

The packages share the `agentacp.v1` contract. Keep compatible additions within that namespace and release changed language packages independently. The Go module currently uses the local development path `agent-acp.local/sdks/go`. Set the final hosting path in the Go module, schema and Go generator options before publishing it, then regenerate. The package manifests are local development manifests. A registry scope or repository URL can be chosen when publishing is authorized.

To add a language or change an operation, follow the [contributor instructions](AGENTS.md).
