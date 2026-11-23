# Live session example

Run one session from a terminal or drive the same executable from a program over JSON Lines. Claude and Codex share the same Go caller. The selected profile supplies an adapter executable and a passive reader for the provider’s native history.

## Start a session

Use an account already signed in to the provider on this host. From the SDK repository root, install the pinned local adapters (Claude 0.109.1 and Codex 2.9.0) with Node.js 22+ and build the example with Go 1.23+. The install is optional when compatible executables and their native history dependencies are already available.

```sh
npm ci --prefix harnesses/local
export AGENT_ACP_HARNESS_DIR="$PWD/harnesses/local"
export PATH="$AGENT_ACP_HARNESS_DIR/node_modules/.bin:$PATH"
cd sdks/go
go build -o /tmp/agent-session-example ./cmd/session-example
/tmp/agent-session-example -provider claude -instance claude-local -cwd /absolute/path/to/project
```

For Codex, use `-provider codex -instance codex-local`. Keep the instance name stable for each account and host. An optional `-model` selects a model when opening a session. To run a particular adapter build, pass its executable path and arguments after `--`. A custom adapter can be used for live sessions without a provider profile.

The first output record has type `session`. Save its `data.ref.sessionId` and the instance name to reopen the conversation. Capabilities appear under `data.capabilities`.

## Send, answer and interrupt

Enter one JSON object per line. This command sends a two-line prompt. Its dollar sign and quotes reach the harness literally.

```json
{"op":"send","id":"turn-1","text":"Read both lines exactly.\nThe literal characters are $HOME and 'single quotes'."}
```

A `receipt` reports the operation ID and delivery status. Watch `observation` records for streamed text, tool activity, input requests and the end of the turn. A receipt with status `DELIVERY_STATUS_ACCEPTED` means the runtime accepted the operation. The later observations tell you what the provider did.

When an observation contains `data.input`, review its prompt and offered choices. Supply the observed request ID and the ID of the choice you select. The example below uses placeholders for both IDs. The current Go runtime handles provider permission prompts using their offered option IDs. Choose the option whose label matches the decision you intend.

```json
{"op":"answer","id":"answer-1","request":"<data.input.requestId>","option":"<selected data.input.choices[].id>"}
```

Interrupt the active turn by its observed `data.turnId`. For turns started by this runtime the turn ID equals the send operation ID. Watch for the subsequent `data.turnEnded` record.

```json
{"op":"interrupt","id":"stop-1","turn":"turn-1"}
```

Use a fresh operation ID for each new operation. A controller can keep stdin open, read stdout one record at a time, and send its next command when the matching observation arrives. Invalid commands produce a correlated `error` record. Send `{"op":"quit"}` when finished. End-of-file waits for a submitted turn to end and exits with an error if a decision still needs input.

## Send literal files or stdin

```sh
/tmp/agent-session-example -provider codex -instance codex-local -cwd /absolute/path/to/project -prompt-file ./prompt.txt
/tmp/agent-session-example -provider codex -instance codex-local -cwd /absolute/path/to/project -prompt-file - < ./prompt.txt
```

Both forms preserve whitespace, shell punctuation and trailing newlines in UTF-8 text. With a file path, stdin remains open for decisions. With `-prompt-file -`, stdin supplies the entire prompt and ends at EOF. Use the interactive form for turns that may request input.

## Reopen or inspect a conversation

Start a fresh process using the original working directory, profile and instance.

```sh
/tmp/agent-session-example -provider claude -instance claude-local -cwd /absolute/path/to/project -resume "$session_id"
/tmp/agent-session-example -provider claude -instance claude-local -cwd /absolute/path/to/project -history "$session_id"
```

Use the same project path when reading Claude history. The Claude reader resolves `@anthropic-ai/claude-agent-sdk` through `AGENT_ACP_HARNESS_DIR` or the installed adapter. The Codex reader uses the `codex` executable in `PATH` or the path supplied in `CODEX_PATH`.

Resume opens a live session and accepts the same commands. History mode calls the SDK’s read-only history operation, follows its pages, prints one `history` record per retained observation and exits. Historical input requests are informational. Retention and the available message details depend on the provider’s native store.


## Verification on macOS

The following checks used the pinned adapters with authenticated local accounts and disposable project directories. A session-list query filtered to a fresh directory completed on both providers. Automated subprocess tests separately exercise stale request IDs, cross-session replies, operation retries after an uncertain delivery, cursor gaps, and cancellation ordering.

| Real provider | Observed result |
| --- | --- |
| Claude Code | Streamed a response to a multiline prompt. A real write requested permission. Selecting the one-time allow choice created the requested fixture. A targeted interrupt ended a later turn with `cancelled`. A fresh process recovered the original prompt byte for byte through native history and resumed the same session ID. |
| Codex | Streamed a real reply. A write from a read-only session presented a permission request. Selecting `cancel` ended the turn and the fixture file remained absent. A separate targeted interrupt ended its specified turn with `cancelled`. Native history returned the exact multiline prompt and assistant reply. A fresh process resumed the same native session ID. |

To reproduce the Claude permission check, use an empty disposable project and place this policy in its `.claude/settings.local.json` before opening the session. It applies to the verification project.

```json
{
  "permissions": {
    "defaultMode": "default",
    "ask": ["Write", "Edit", "Bash"]
  }
}
```

Send a prompt asking Claude to create one small file in that directory. Wait for the input observation, answer with the offered one-time allow ID, and check the file contents after the turn ends. To reproduce the interrupt, submit another turn and send the interrupt while that turn is active. After quitting, use `-history` to inspect the retained prompt, then `-resume` to continue the conversation.

For the Codex permission check, launch the example in the disposable project with the adapter’s read-only mode.

```sh
INITIAL_AGENT_MODE=read-only /tmp/agent-session-example -provider codex -instance codex-local -cwd "$PWD"
```

Ask Codex to request permission to create a fixture file outside its read-only permissions. When the request appears, select the offered cancellation option and verify the file is absent. This explicit permission boundary matters because the adapter can automatically review low-risk actions in its default mode.
