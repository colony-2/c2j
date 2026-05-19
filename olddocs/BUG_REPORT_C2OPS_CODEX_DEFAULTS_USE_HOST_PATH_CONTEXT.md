# Bug Report: c2ops Codex defaults still use host-view path context

## Summary

The c2ops Codex op still defaults its path inputs from the legacy host-view
context aliases:

```yaml
workdir_path:
  default: "{{ context.environment.workdir }}"
worktree_path:
  default: "{{ context.environment.worktree_path }}"
artifact_inbox_path:
  default: "{{ context.environment.inbox }}"
artifact_outbox_path:
  default: "{{ context.environment.outbox }}"
```

Current recipe guidance says selector-backed ops should use
`context.environment.op.*` for paths consumed by the running op process.

## Environment

- Date detected: 2026-05-15 at 23:27:00 UTC
- c2ops checkout: `/c2ops`
- c2ops commit observed: `9d02c2e` (`remove shai from codex op, rely on extension framework.`)
- File observed: `/c2ops/codex/op.yaml`

## Expected Behavior

c2ops Codex should default to op-visible paths:

```yaml
workdir_path:
  default: "{{ context.environment.op.workdir }}"
worktree_path:
  default: "{{ context.environment.op.worktree_path }}"
artifact_inbox_path:
  default: "{{ context.environment.op.inbox }}"
artifact_outbox_path:
  default: "{{ context.environment.op.outbox }}"
```

This should work for both no-sandbox and sandboxed execution, with c2j choosing
the correct process-visible paths.

## Actual Behavior

Codex defaults still use `context.environment.*` host-view aliases. Recipes must
pass all Codex path inputs explicitly to avoid depending on outdated defaults.

## Impact

- Recipe authors can accidentally pass host-only paths into extension processes.
- Sandbox/no-sandbox behavior differs from the documented op-visible path
  contract.
- Live recipe tests are harder to reason about because Codex path behavior
  depends on whether recipes remembered to override every path input.

## Current Recipe Workaround

The recipes in this repo now pass the path inputs explicitly:

```yaml
worktree_path: "{{ context.environment.op.worktree_path }}"
workdir_path: "{{ context.environment.op.workdir }}"
artifact_inbox_path: "{{ context.environment.op.inbox }}"
artifact_outbox_path: "{{ context.environment.op.outbox }}"
```

The c2ops default should still be fixed so future recipes do not need this
boilerplate.
