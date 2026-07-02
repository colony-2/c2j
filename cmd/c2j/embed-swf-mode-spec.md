# Proposal: `c2j` Embedded JobDB Mode via `embed:///`

## Summary

`c2j` should support an embedded JobDB runtime selected with:

```text
--jobdb embed:///
```

This mode is for local testing and development. It runs the JobDB SQLite runtime
in-process inside `c2j` instead of talking to a separately managed remote HTTP
server.

## Behavior

- `http://host/tenant` and `https://host/tenant` select a remote runtime and tenant.
- `embed:///` starts an embedded runtime owned by the current `c2j` process.
- embedded mode always uses tenant `0`.
- Embedded runtime state is persisted on disk so separate `c2j` invocations can see the same jobs.

## Storage

Default root:

```text
~/.c2j/embed/default
```

Layout:

```text
<root>/
  lock
  swf.db
  swf.db.blobs/
```

## Implementation

`c2j` owns a small embedded bootstrap in `cmd/c2j/internal/swfruntime`.

For `embed:///` it:

1. Resolves the embedded root.
2. Acquires an exclusive lock file.
3. Opens `<root>/swf.db` with `github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite`.
4. Uses the SQLite runtime's embedded Strata rowstore and blobfs artifact
   storage.
5. Builds a JobDB-backed workflow engine from that runtime.

This intentionally does not start an HTTP server. `c2j` uses the SQLite runtime
in-process.

## Command impact

- `submit` uses the shared runtime opener instead of constructing `remoteruntime` directly.
- `run`/`run one` uses the shared runtime opener and passes the resulting workflow runtime into `swf.GetJobForRun(...)`.
- `list` uses the shared runtime opener and engine for `ListJobs(...)`.

CLI text should describe `--jobdb` as:

```text
JobDB URI (http(s)://host/tenant or embed:///)
```

## Locking

Embedded roots are single-owner in v1.

If another process tries to open the same root while it is already in use, `c2j` fails fast.

## Testing

Minimum coverage:

- runtime persistence across reopen for `embed:///`
- submit + run end-to-end using the embedded runtime
- existing remote-mode tests remain unchanged
