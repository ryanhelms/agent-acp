# Session conformance

`session.json` is a script of public SDK calls. Every binding replays the same ordered steps against an injected deterministic runtime. Requests and replies use the standard Protobuf JSON representation. A unary operation has one reply. An observation stream has zero or more replies in delivery order.

The script starts with a history read on a remote session, then opens a local session with the same native ID. It exercises literal multiline input, a correlated approval, opaque event data, a targeted interruption with uncertain delivery and an opaque pagination cursor. Reading history dispatches only the history operation.

To add a language, implement a scripted runtime that checks the operation and request at its boundary and returns the listed replies. Invoke the methods on the generated SDK, compare all responses with the fixture, check stream cleanup or cancellation, and fail on extra or missing operations. These tests exercise the public binding. Provider adapters require separate integration tests with the installed harness.
