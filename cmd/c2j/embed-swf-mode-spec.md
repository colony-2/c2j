# Proposal: `c2j` Embedded SWF Mode via `embed:///`

## Summary

`c2j` should support an embedded SWF runtime selected with:

```text
--swf-url embed:///
```

This mode is for local testing and development. It runs the SWF direct runtime stack in-process inside `c2j` instead of talking to a separately managed remote HTTP server.

## Behavior

- `http://...` and `https://...` keep their current meaning.
- `embed:///` starts an embedded runtime owned by the current `c2j` process.
- Embedded runtime state is persisted on disk so separate `c2j` invocations can see the same jobs.

## Storage

Default root:

```text
~/.c2j/embed/default
```

Override for tests or advanced use:

```text
C2J_EMBED_ROOT=/abs/path
```

`C2J_EMBED_ROOT` must be an absolute path.

Layout:

```text
<root>/
  lock
  postgres/
    runtime/
    data/
  strata/
    rows/
    blobs/
```

## Implementation

`c2j` owns a small embedded bootstrap in `cmd/c2j/internal/swfruntime`.

For `embed:///` it:

1. Resolves the embedded root.
2. Acquires an exclusive lock file.
3. Starts embedded Postgres using persistent directories under the root.
4. Installs/verifies pgwf schema.
5. Starts embedded Strata using:
   - `pebble://<root>/strata/rows`
   - `blobfs://<root>/strata/blobs`
6. Builds `directruntime.NewFromConfig(...)`.
7. Builds a SWF engine from that runtime.

This intentionally does not start an HTTP server. `c2j` uses the direct runtime in-process.

## Command impact

- `submit` uses the shared runtime opener instead of constructing `remoteruntime` directly.
- `exec` uses the shared runtime opener and passes the resulting `swf.WorkflowRuntime` into `swf.GetJobForRun(...)`.
- `list` uses the shared runtime opener and engine for `ListJobs(...)`.

CLI text should describe `--swf-url` as:

```text
SWF runtime URL (http(s)://... or embed:///)
```

## Locking

Embedded roots are single-owner in v1.

If another process tries to open the same root while it is already in use, `c2j` fails fast.

## Testing

Minimum coverage:

- runtime persistence across reopen for `embed:///`
- submit + exec end-to-end using the embedded runtime
- existing remote-mode tests remain unchanged
