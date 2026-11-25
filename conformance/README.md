# Session conformance

`session.json` is a script of public SDK calls. Every binding replays the same ordered steps against an injected deterministic runtime. Requests and replies use the standard Protobuf JSON representation. A unary operation has one reply. An observation stream has zero or more replies in delivery order.

The script starts with a history read on a remote session, then opens a local session with the same native ID. It exercises literal multiline input, a correlated approval, opaque event data, a targeted interruption with uncertain delivery and an opaque pagination cursor. Reading history dispatches only the history operation.

To add a language, implement a scripted runtime that checks the operation and request at its boundary and returns the listed replies. Invoke the methods on the generated SDK, compare all responses with the fixture, check stream cleanup or cancellation, and fail on extra or missing operations. These tests exercise the public binding. Provider adapters require separate integration tests with the installed harness.

## Adapter conformance

The Go SDK package `agent-acp.local/sdks/go/conformance` runs the same session assertions against any `agentacp.Runtime`. A Go adapter author calls `conformance.Check(ctx, runtime, target, scenario)` with a bounded context and a disposable workspace. Executable adapters use the companion ACP client command.

```sh
cd sdks/go
go run ./cmd/adapter-conformance -cwd /absolute/path/to/fixture -fixture /absolute/path/to/scenario.json -- /absolute/path/to/adapter
```

An adapter advertising session resume must demonstrate recovery on another connection. The executable checker closes its original runtime and adapter processes, starts a fresh runtime with the same workspace and harness configuration, and resumes an original fixture session. It runs the addressed interrupt scenario on that resumed session. A Go adapter advertising durable resume uses `conformance.CheckWithRestart(ctx, runtime, target, scenario, restart)`. Its restart callback closes the original binding and returns a new runtime initialized from the retained provider state. The caller owns cleanup. A still-working original binding or a missing restart callback fails the check. An adapter without durable resume can continue to use `Check`.

Use a fixture model or an explicit test conversation. The driver opens two sessions, sends the same literal fixture prompt, closes and reconnects each observation stream, and replies to the addressed permission. The scenario names the exact permission title and one-time option label. It expects the resulting tool marker and assistant confirmation. A second prompt must continue streaming after the interrupt marker so the driver can address a stale turn and then the active turn. When history is advertised, the fixture supplies a known retained marker and the driver reads it through the SDK without opening a new live session. The command accepts `-history-provider claude` or `-history-provider codex` to configure those optional readers.

```json
{
  "Prompt": "  Read the fixture. Preserve $HOME and quotes.\n",
  "PermissionPrompt": "Allow reading file?",
  "ChoiceLabel": "Allow once",
  "ToolResultMarker": "fixture bytes",
  "CompletionMarker": "permission received",
  "InterruptPrompt": "wait",
  "InterruptReady": "waiting"
}
```

`ToolResultPhase` defaults to `completed`. `HistoryMarker` is required for a runtime that advertises passive history. `ExtensionType` and `ExtensionDataMarker` can require a retained provider payload such as an unavailable-resource record. The suite checks session and cursor ownership, command receipts and retries, permission routing, tool results, stream reconnection, targeted cancellation and capability behavior. It exercises an embedded Go fixture and a separate line-delimited ACP process. The language binding fixture still tests the independent public packages.

When the adapter advertises session listing, the driver reads the selected workspace one session per page and requires both completed fixture sessions under their original IDs. Additional sessions from the workspace are allowed. When listing is unavailable, the list call must return an error.

For each advertised media type, configure the deterministic fixture backend to recognize `Fingerprint the attached conformance image.` or `Fingerprint the attached conformance audio.` followed by one inline PNG or WAV respectively. The backend reads the actual bytes and returns one assistant message containing `sha256:` followed by their lowercase hexadecimal SHA-256 digest. The driver compares this value with its original media and requires a completed turn under the same session and operation. The media fixture uses ordinary prompt and message types. It runs at the backend that receives the attachment after translation. Unadvertised media must produce an error or an `UNSUPPORTED` receipt for the operation.
