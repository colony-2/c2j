<!-- Sources: README.md; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/listjobs/options.go; cmd/c2j/internal/workjob/options.go. -->

# Job Workflows

## Local Submit And Run

```bash
c2j submit "Implement the endpoint" --run --embed
c2j submit "Improve retry behavior" --evolve --run --embed
```

These resolve `build` (default) or `evolve` in the target cell, falling back to the shared recipes when absent. Embedded runtime avoids a remote JobDB. For custom local recipe authoring, use `--advanced-recipe-file ./recipes/my-recipe.yaml` instead of a mode flag and declare `prompt` in the recipe's input schema.

## Submit Then Continue Later

```bash
c2j submit "Implement the endpoint" --embed --json
c2j run --job-id <job-id> --embed
```

Use JSON output to capture the submitted job identity. Use `c2j list --self --embed` if the job ID is not known.

## Ready And Worker Commands

Use `ready` to count claimable work for schedulers:

```bash
c2j ready --embed
```

Use worker commands to run claim loops:

```bash
c2j run one --embed
c2j run any --embed
c2j run loop --embed
```

Prefer bounded local concurrency and explicit tenant/cell targeting in automation.

## Execute a supplied lease

Use this mode when a dispatcher already owns the lease and has handed execution
to this worker. The dispatcher uses JobDB `remote.ExportLease` and `Encode` to
produce the capability, then stops executing/renewing before the receiver starts.

```bash
c2j run with-lease --jobdb https://jobdb.example.com/tenant --job-id JOB_ID --lease-file /run/secrets/job-lease.json
```

Use an owner-only regular file (mode `0600` or `0400`) or protected stdin with
`--lease-file -`. Do not inspect/print the credential in logs or pass its value
as a command-line argument. The CLI validates the configured tenant/job match,
and JobDB validates and renews the lease before work and throughout execution.

The invocation exits on completion, ordinary rescheduling, environment handoff,
input wait, or lease failure. It never polls for or acquires a replacement lease.
Suspension/environment handoff exits 0, input-required exits 3, and invalid/lost
leases or transport failures exit nonzero. Answer input separately and let the
dispatcher supply the next lease; do not automatically switch to `c2j run`.

Upgrade the JobDB service/backend to support v0.0.22 authoritative renewal before
using the updated remote worker. Unsupported renewal is an error, not a reason
to bypass validation. c2j's Go library also accepts already-held embedded leases.

## Input Modes

`c2j run` supports:

- `prompt`: ask interactively for user input.
- `ops`: allow input collection through operation mechanisms.
- `fail`: fail when input is required.

In CI or non-terminal stdin, the default input mode is `ops`.

## Exit Behavior

Command services return non-zero exit codes for usage errors, compile/test failures, and run failures through typed exit errors. When scripting, prefer `--json` outputs where available and check process status.

## Child Jobs

Use child listing commands to inspect jobs started by a parent:

```bash
c2j list children --jobdb https://jobdb.example.com/dev --parent-tenant-id dev --parent-job-id <job-id> --all-ops
```

In recipes, child jobs started through recipe ops and child groups are also exposed in runtime context where supported by the worker.

Inside an op, `c2j list children` uses the injected parent and invocation context. Use `--all-ops` to include other invocations and `--all` to fetch all pages. Terminal jobs are excluded by default; select `--status COMPLETED,CANCELLED` to find finished children. Current-job environment alone supplies attribution metadata; the `C2J_CHILD_JOB_*` broker environment provides formal lease-scoped parentage. See `C2J_CHILD_JOBS_GUIDE.md` in the c2j repository for the complete guide.
