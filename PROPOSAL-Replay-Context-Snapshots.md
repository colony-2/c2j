# Replay context implementation

Implemented with JobDB `v0.0.65-0.20261029235121-6d86c38fc234`. Its scope callbacks and input builders run synchronously; task workers remain asynchronous.

## Ownership and writes

Serial orchestration owns the resolver. Task workers receive serialized inputs and return results; they never receive a resolver. Task completion decoding and Git/artifact bookkeeping happen only after `DoTask` accepts the result. A canceled task can finish late without publishing context data.

A scope body prepares one `ExecutionOutput`. The caller publishes it only after `DoScope` accepts that outcome. A nested op/composite inside a state forwards its output to the owning state, which publishes after its own acceptance. Catch outputs use the same boundary. Validation publishes directly.

Each invocation has one completion position. A second publication returns `ErrAlreadyWritten`. A later visit to the same logical state has a fresh position. Existing native maps retain their reader shape: publication advances the latest output and appends the prior result to `.runs`. Stored payloads are immutable. This preserves CEL and Go-template compatibility without custom readers, version numbers, or a persistent map library.

Inputs, variables, locals, transition payloads, failures, and outputs are shared as immutable values. Lexical shadowing builds a new binding map without deep-copying inherited payloads. Execution bookkeeping remains mutable under serial orchestration. The implementation does not introduce a generic encapsulated store or prevent arbitrary callers from mutating the historically exported `TemplateData`; executor publication is the controlled write boundary.

## Cloning

Ordinary scope entry creates only a shallow resolver header (`ExecutionView`), sharing maps and bookkeeping. Task input builders use the current resolver directly. Adoption retains the accepted header's bindings without traversing ancestors or copying maps.

`CloneForRestart` is called only by `OnRestart`, before applying a restart patch. `AdoptRestart` installs that graph while preserving enclosing map references. No ordinary context graph snapshots remain. Input/output serialization, native reader conversions, diagnostic snapshots, and new lexical binding construction still allocate; these are not context isolation copies.

The context-bound writer locks and revocation machinery are unnecessary with serial scope ownership and have been removed. The operation task contract remains version 1 for replay compatibility.

## Verification

- Full template, compiler, story/live, and story suites pass with the new dependency.
- Targeted race-detector checks pass, including retry/restart, catches, workspaces, publication, and late task completion. Every repository package compiles with `go test ./... -run '^$'`.
- Tests cover duplicate completion; rejection after preparing outputs for ops, sequences, machines, and states; nested-op publication owned by its state; late canceled task results; restart patch isolation; nested retry/restart and replay; workspace Git and artifact behavior.
- A regression detects serialization of unused prior outputs during ordinary scopes and task input builders, and checks their identity through execution and replay.
- The supplied 162-chapter job reconstructs in **1.879 seconds**, with no replay error and its recorded failed status. The initial baseline was **70.824 seconds**, approximately **38x slower**. This is a local single-run comparison, not a controlled benchmark.

See [the copy analysis](REPLAY-COPY-ANALYSIS.md) for the original measurements and reproduction artifacts.
