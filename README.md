# agent-acp

agent-acp gives applications one typed session API for coding harnesses. The Go, TypeScript and Python packages share a versioned schema and a conformance fixture. Applications can open or resume sessions, send literal text, observe a stream, answer a specific request, interrupt a turn, and query native history.

This repository currently provides generated clients, models and an adapter interface. The included executable examples use scripted runtimes. Live Claude and Codex runtimes are the next integration work.

## Repository layout

| Path | Responsibility |
| --- | --- |
| [schema/agentacp/v1/session.proto](schema/agentacp/v1/session.proto) | Public operation names, request and response types, event variants and delivery outcomes |
| [sdks/go](sdks/go) | Go module with context-aware calls and an interruptible observation iterator |
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

History belongs to the harness. Reading history is a passive operation, and live subscriptions have their own lifetime. Closing an observer releases that subscription. Interrupting a turn is a separate command with an explicit session and turn ID.

## Package releases

The packages share the `agentacp.v1` contract. Keep compatible additions within that namespace and release changed language packages independently. The Go module currently uses the local development path `agent-acp.local/sdks/go`. Set the final hosting path in the Go module, schema and Go generator options before publishing it, then regenerate. The package manifests are local development manifests. A registry scope or repository URL can be chosen when publishing is authorized.

To add a language or change an operation, follow the [contributor instructions](AGENTS.md).
