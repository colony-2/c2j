# Remaining `command_execution` challenge catalog

This catalog only lists the remaining `command_execution` uses that still represent a structural gap (vs. simple glue already removed).

| recipe | op name/path | what command is doing | alternative pattern (optional) |
|---|---|---|---|
| `job-validate` | `job-validate.yaml:18` (`state.states.detect`) | Detects suggested validation commands from repo markers (`package.json`, `go.mod`, `Cargo.toml`, etc.). | Move to cell/project-level validate config or a dedicated `validate.detect` op. |
| `job-validate` | `job-validate.yaml:102` (`state.states.run_provided`) | Runs provided validation commands, captures `validation/output.txt`, and writes `validation/output-tail.txt` artifact for UI display. | Dedicated `validate.run` op with controlled command policy and built-in artifact capture. |
| `job-validate` | `job-validate.yaml:136` (`state.states.run_suggested`) | Runs auto-detected validation commands, with full log + tail artifact capture in outbox. | Same `validate.run` op; pass a `mode: suggested` input. |
| `job-validate` | `job-validate.yaml:170` (`state.states.run_custom`) | Runs custom user-specified validation commands, with full log + tail artifact capture in outbox. | Same `validate.run` op with allowlist/policy guardrails. |

## Summary

Remaining command-op usage clusters into one real gap:

1. **Validation execution gap**  
   Validation still needs shell execution/capture. This is the biggest remaining structured-op gap and where a `validate.run` op would remove most command execution usage.

Net: orchestration logic uses codex/artifact handoff patterns; remaining command usage is concentrated in validation execution.
