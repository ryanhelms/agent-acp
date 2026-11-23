# Developing agent-acp

Work on the portable SDK in this repository. The public vocabulary is sessions, turns, requests, observations, capabilities and receipts. Applications supply their own identities, worker trees, authorization policy and UI.

## Change a shared operation

1. Edit `schema/agentacp/v1/session.proto`. Use typed fields and opaque session, request, operation and cursor identifiers. Keep a field's meaning and number stable across compatible changes. Reserve removed field names and numbers. For a breaking change to a published contract, introduce a new versioned schema package.
2. Preserve the distinction between accepting a command, delivering it and observing its outcome. Treat an uncertain delivery as an operation to reconcile. Preserve literal prompt contents and route replies to their original session and request. Reading historical requests must be passive.
3. Update the conformance fixture and the cases that exercise the changed behavior. Change a generator template only when the language mapping needs to change. Keep provider-specific behavior in runtime adapters.
4. Run `make generate` and `make check`. Include changed generated files with their schema change so consumers can build the package using ordinary language tooling.

## Add a language

- Create `sdks/<language>/` with an idiomatic package manifest. Use a standard, pinned Protobuf generator for models. Register its target in `tools/codegen.py` with a deterministic output directory and include it in the full generation and drift checks.
- Use the compiled service descriptor to enumerate the operations. Add a facade template under `tools/facades/` and its rendering rules to the facade generator. Follow an existing language target all the way from a request type to the generated call. Only the runtime needs a unary dispatch and a streaming dispatch. Generated public methods carry native types and cancellation conventions.
- Exercise the new public package against `conformance/session.json` using an injected scripted runtime. Verify session identity, whitespace, request correlation, ordered streaming, cancellation or early closure, and passive history reads. Include the language's build and tests in `make check`.
- Run generation twice and verify clean output. Document any toolchain requirement in the root README. If a runtime or language feature cannot express part of the contract, surface the limitation explicitly.

## Repository workflow

This repository has its own release lifecycle. Worktree checkouts belong under `~/.worktrees/agent-acp/<worktree-name>/`. Keep tools, templates and generated source here. Agent Commander and other consumers depend on released packages or a local development workspace.
