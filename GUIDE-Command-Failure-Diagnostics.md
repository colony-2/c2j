# Command failure diagnostics

A native `command_execution` failure exposes versioned evidence in
`failure.attrs.command`, including in catches on enclosing includes and states:

```yaml
failure:
  kind: task_error
  message: 'command execution failed: exit status 7'
  node: # The original command node, preserved through enclosing catches.
    id: check_tools
    op: command_execution
  attrs:
    command:
      version: 1
      exit_code: 7
      timed_out: false
      diagnostics:
        _c2j/command/stdout.txt: <stored ArtifactRef>
        _c2j/command/stderr.txt: <stored ArtifactRef>
```

Both streams are stored, including empty streams. Artifacts preserve trailing
newlines and other stream bytes after the existing secret redaction rules.
The normal `stdout` and `stderr` output fields still trim trailing newlines.
Each stored reference identifies the producing job and task attempt's result
ordinal. Retries produce distinct references; persistence, resume, and replay
retain those references without rerunning completed commands.

`exit_code` is the process exit code, or `-1` if no exit code is available
(for example, a shell that cannot start or a timeout). A command timeout sets
`timed_out: true`, retains partial streams, and has `failure.kind: timeout`.
Cancellation keeps its native cancellation cause. Diagnostic collection errors
are reported in optional `diagnostic_errors`; they do not replace the command
failure. A reference is omitted if that artifact cannot be attached or its
durable identity is unavailable.

Input validation failures before command execution do not have stream evidence.
Success and explicit `continue_on_error` keep their existing output behavior and
do not add this failure contract. No report-inspection operation is necessary.

## Forwarding evidence to the next operation

This state catch forwards the failure and binds its streams into the next
operation's inbox. The `check` state can also contain nested includes; the
original failing command's identity and references are retained.

```yaml
id: caught-command-diagnostics
version: '1'
input_schema: {}
state:
  initial: check
  states:
    check:
      op: command_execution
      inputs:
        run: 'printf "checking tools\n"; printf "Required tool unavailable: go\n" >&2; exit 7'
      catch:
        - when: failure.kind == 'task_error' || failure.kind == 'timeout'
          to: inspect
          payload:
            failure: '${{ failure }}'
            diagnostics: '${{ failure.attrs.command.diagnostics }}'
    inspect:
      op: command_execution
      artifacts:
        stdout.txt: '${{ transition.payload.diagnostics["_c2j/command/stdout.txt"] }}'
        stderr.txt: '${{ transition.payload.diagnostics["_c2j/command/stderr.txt"] }}'
      inputs:
        run: |
          cat "${{ context.environment.op.inbox }}/stdout.txt"
          cat "${{ context.environment.op.inbox }}/stderr.txt" >&2
          printf 'Original exit code: %s\n' '${{ transition.payload.failure.attrs.command.exit_code }}'
outputs:
  report: '${{ states.inspect.outputs.stdout }}'
```

Recipes can forward the same refs to a repair agent using ordinary artifact
bindings. Keep stream contents in artifacts rather than copying them into catch
payloads or error messages.
