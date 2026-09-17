# Execution allocation inputs

This is the shared input parser for the pending execution-requirements
integration. It is tested independently but **not yet registered on run or list
commands**. Wiring depends on runtime support; no command currently promises
enforcement of these new inputs.

Bind `Options.AddFlags` to a command's local flag set. At execution time, pass
`os.LookupEnv` to `Options.Parse`, then inject the resulting versioned allocation
into the worker. Argument values override the corresponding environment field
independently, allowing mixed sources. Explicit empty arguments or environment
values are invalid; omitted values remain unknown. Parsing does not inspect
host capacity or use recipe requests as evidence of allocation.

| Argument | Environment | Meaning |
| --- | --- | --- |
| `--execution-cpu` | `C2J_EXECUTION_CPU` | Usable cores/millicores |
| `--execution-memory` | `C2J_EXECUTION_MEMORY` | Usable memory |
| `--execution-ephemeral-storage` | `C2J_EXECUTION_EPHEMERAL_STORAGE` | Usable scratch |
| `--execution-platform` | `C2J_EXECUTION_PLATFORM` | OS/architecture/optional variant |
| `--execution-image` | `C2J_EXECUTION_IMAGE` | Launch reference/tag |
| `--execution-image-digest` | `C2J_EXECUTION_IMAGE_DIGEST` | Actual manifest digest |
| `--execution-image-id` | `C2J_EXECUTION_IMAGE_ID` | Runtime/config identity, diagnostic only |

List commands should call `ParseFilter` with their explicit
`--compatible-with-execution` and `--include-unresolved` choices. Without
compatibility opt-in, environment variables are not read and allocation flags
are rejected. With opt-in, at least one compatibility-relevant allocation fact
is required; an image ID alone is insufficient. `IncludeUnresolved` admits
unknown jobs for default-environment bootstrap, not as proven compatible jobs.
This package parses the candidate; it does not perform listing or pagination.

Run `go test ./cmd/c2j/internal/executionflags` for precedence, unknown-input,
validation, image-identity, and list opt-in tests.
