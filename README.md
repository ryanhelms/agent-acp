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

Use the existing `SendRequest.text` field for a plain prompt or `parts` for ordered text, inline images, and inline audio. The Go ACP runtime exposes the peer's image and audio capabilities on the opened session and rejects unavailable media before sending the prompt. User previews show media descriptors. The request carries binary data as Protobuf bytes. [ACP content blocks](https://agentclientprotocol.com/protocol/v1/content) · [ACP capability negotiation](https://agentclientprotocol.com/protocol/v1/initialization)

Configure each Go runtime with a stable harness instance, an adapter command and arguments, and an absolute working directory. Pass it to `agentacp.New`. The adapter starts on demand. Register stdio MCP servers through `runtime.Config.MCPServers`. Set `OncePermissionsOnly` to restrict permission prompts to one-time decisions. The runnable example shows the complete call sequence. [Go example entry point](sdks/go/cmd/session-example/main.go)

When an ACP update carries a direct MCP result or a JSON-RPC tool reply in `rawOutput`, the Go runtime keeps its failure status and available display content together. The preview marks structured results and attachments. The original fields are available to the scoped source hook and ACP replay. [MCP tool results](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-result) · [ACP tool calls](https://agentclientprotocol.com/protocol/v1/tool-calls)

| Operation | Go runtime behavior |
| --- | --- |
| Open or resume | Each live session owns an adapter process. The provider issues the session ID and advertises its ability to resume and list sessions. |
| Send and observe | One active turn per session. Literal text travels over a structured message. A send operation ID identifies the turn, and observations carry the originating session and cursor. |
| Answer or interrupt | A permission answer names the session, live request, and one of its offered choices. An interrupt targets the active turn by ID. Observe its end to confirm cancellation. |
| Reconnect an observer | Supply its last cursor. A gap signals that events fell outside the in-memory window or the live connection was replaced. Closing the observer releases the subscription. |
| Browse and read history | `ListSessions` queries the live adapter’s native listing. `ReadHistory` uses the configured passive reader. Claude reads retained messages through the Claude Agent SDK. Codex uses App Server’s `thread/read` and `thread/turns/list`. |

Passive Claude messages and Codex items retain their original JSON fields and native IDs. An observation's `sourceCursor` identifies the record in the same provider history. Its `sourceRecord` holds the original bytes, or its ordinary `extension` holds them when the event already carries the original. Multiple observations from a message can refer to one original on an earlier or later page. Use the response's `nextCursor` to continue paging. Codex turn metadata is retained separately from the ordered items it describes.

A workspace-scoped `ListSessions` checks each agent-supplied primary working directory before returning a session. An existing directory may match by filesystem identity, including a symlink alias. For removed directories the exact absolute path must match. Entries with missing or unrelated directories are excluded. A filtered native page may be empty and still carry a `nextCursor`. Continue until the cursor is absent. [ACP session listing](https://agentclientprotocol.com/protocol/v1/session-list)

Native storage and retention remain under provider control. A history-only runtime can omit the live adapter command. Live delivery receipts are held in memory during the runtime’s lifetime. Applications that need operation reconciliation across process restarts own their operation IDs and receipt records.

## Retained ACP replay in Go

Set `runtime.Config.ReplayHistory` for an adapter that supports standard ACP
session loading. Pass an inactive session handle and its original working
directory. The reader loads it in a disposable process with an empty MCP
server list and cancels historical permission requests. It never sends a
prompt or publishes a historical approval. The runtime blocks concurrent
resume of the same session through its own handle while replay is in progress.

`ReadHistory` pages normalized observations and original ACP source records.
The cursor identifies the session and a hash of the replayed prefix. A changed
prefix invalidates older pages. A custom `ReadHistory` function takes
precedence when the host supplies a passive vendor reader. The host must
prevent replay while another process still controls the conversation.

## Package releases

The packages share the `agentacp.v1` contract. Keep compatible additions within that namespace and release changed language packages independently.

Build a local release from an SDK checkout. The release builder stages the tracked source files, keeping local dependency directories and credentials outside the artifacts. Install the pinned Python wheel tools in an isolated environment and pass a new output directory.

```sh
python3 -m venv /tmp/agent-acp-release-tools
/tmp/agent-acp-release-tools/bin/python -m pip install setuptools==80.9.0 wheel==0.45.1
/tmp/agent-acp-release-tools/bin/python tools/release.py 0.1.0 dist/agent-acp-0.1.0
```

The output contains a Go module proxy directory for `agent-acp.local/sdks/go@v0.1.0`, an npm tarball, a Python wheel, the shared conformance fixture and a SHA-256 manifest. The Go archive includes the Go facade's conformance fixture. The npm and Python artifacts provide their generated clients and runtime interfaces. Set a fixed `SOURCE_DATE_EPOCH` for reproducible builds or use the default SDK commit timestamp.

Use the proxy's `go/` directory as a `file://` Go proxy. For a new verified candidate, record the Go module and module-file hashes in the consumer's `go.sum`. Put the private module in `GONOSUMDB`. Go checks the consumer's pinned sums. Install the tarball with npm and the wheel with pip. Test a consumer outside the SDK checkout before distributing the files.

A registry scope or final repository URL can be chosen when publishing is authorized. Preserve the stable `agentacp.v1` schema when changing distribution names.

To add a language or change an operation, follow the [contributor instructions](AGENTS.md).

### Session update metadata

An ACP `session/update` can include additional fields beside `sessionId` and `update`. The Go runtime retains the full params for the bound session at the observation’s own cursor. Text and tools carry an `acp.session_update_params` source record. Other variants, including explicitly null or missing updates, can be inspected as passive extensions. Ordinary updates keep their existing shape. In the optional Go callback, `Body` is the inner update and `Params` contains the enriched params. `ParamsSize` identifies enriched updates even when the full evidence exceeded its byte budget. Treat the callback and observation as two ways to reach the same source at the same cursor.

Passive ACP replay applies the same source type and uses its existing replay limits. Source omission carries the reason and original byte count. Cursors include enriched source values and tolerate reordered JSON object members between replay passes.


## Scoped JSON-RPC provenance

The Go runtime uses the independently versioned `agent-acp.local/transport v0.1.0` module. For an offline installation, make both local release proxies available to Go and pin their module sums. The TypeScript and Python clients consume the same generated observation schema.

An observation may carry `wireProvenance` for a selected permission request, a prompt response, or a session update with an extended envelope. It identifies the JSON-RPC message kind, method and protocol version. A reply names its correlated prompt method. The original payload stays at `sourceCursor`, in either `sourceRecord` or the passive extension. Incoming IDs identify the native wire request; calling `Answer` uses the separate SDK input request ID.

`idJson` contains the original JSON token as text. A string ID retains its quotes, a numeric ID preserves every digit, and explicit JSON null appears as the four characters `null`. An absent ID leaves the field unset. `additionalFieldsJson` is a JSON object containing the envelope’s additional members in wire order. It preserves repeated names, numeric spelling and nested values. Consumers can display this text directly. A peer that sends invalid UTF-8 uses the corresponding byte fallback. Oversized components report omitted byte counts. Protocol versions accept up to 4 KiB of encoded input.

Live capture and passive ACP replay apply their existing size limits. Ordinary notifications keep the same typed fast path. A Go update callback receives its own metadata value at the observation’s cursor. Runtime shutdown or cancellation can leave an operation’s delivery unknown. Only an actual matched peer frame supplies wire provenance, including when a peer itself returns a reserved JSON-RPC error code.
