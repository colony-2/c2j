# Restart selection and context inspection

Discover restart boundaries, inspect their context, and submit a corrected
continuation from the CLI:

```sh
c2j job restart-points JOB
c2j job restart-points JOB --json
c2j job restart-context JOB --scope SCOPE --output before.json
c2j restart JOB --scope SCOPE --request-id recovery-1 --patch-file patch.json
```

`restart-points` reads recorded history without replaying or loading the recipe.
Its JSON contains `project_id`, `job_id`, and `restart_scopes`; each point has
`scope_id`, `chapter_ordinal`, `started_at`, and available recorded title/path.
Repeated visits remain separate. A duplicate scope identity is listed with an
`unavailable_reason`, and cannot be selected. An eligible job with no points
returns an empty list. A missing job or denied read returns an error.

Inspection and submission require exactly one explicit `--scope ID` or
`--chapter ORDINAL`. Chapter `0` explicitly selects submission. A chapter with
multiple scopes is ambiguous; use a unique scope ID from discovery. Blank scope
IDs, negative chapters, missing selectors, and both flags are errors.

Inspection writes complete JSON to stdout by default (`--json` is also accepted).
`--output FILE` creates a new file, refusing to overwrite an existing one;
`--output -` writes stdout. It preserves the snapshot fields described below,
including deferred configuration, object, and artifact references and redaction
metadata. A missing recipe can prevent inspection while discovery still works.

Write only intended edits in `patch.json`, for example:

```json
{"scopes":[{"container":"sequence","id":"write","outputs":{"document_path":"design.md"}}]}
```

The step ID is local to `sequence` or `states`, not the opaque restart scope ID.
Optional `job` edits use the typed job-context JSON shape, for example
`{"job":{"git":{"author":"Recovery Author"}}}`. Unknown structural/job
fields, malformed JSON, multiple documents, wrong types, `inputs`, `config`,
`workspace`, and `cell_resolution` edits are rejected. Forbidden identities are
rejected even when their value is null. Arbitrary JSON remains supported inside
output maps and other extensible map values. Null deletes map keys; arrays
replace whole arrays. `{}` is an empty patch. Omitting `--patch-file` preserves
the legacy unpatched restart. Submission accepts only an absent or empty patch.
Do not copy the inspection snapshot or redaction placeholders into a patch.
Runtime validation can still reject a patch when replay reaches its boundary;
a successful submission returns the new identity, not an execution result.

Restart returns a complete JSON job reference. It continues the source goal by
default; `--goal new` creates a new goal. Reuse `--request-id` only to retry the
same selection and payload. Changed requests need a fresh ID. A new patch
replaces the prior patch at that boundary; omitting it removes the prior patch.
Earlier patches in the retained prefix remain effective. The source job stays
unchanged and uncancelled. Tasks and external side effects in the selected
continuation may repeat. After submission, further corrections require another
restart.

Inside a task, these commands use the authenticated worker helper, and `JOB`
can be `self`. They never fall back to a direct connection, including when an
explicit `--jobdb` is supplied. An older helper without the required capability
returns an error. Restart submission requires the operation's `jobs.submit`
grant and permitted source cell. The trusted controller checks these grants and
uses its current owner lease; delegated tokens themselves cannot restart jobs.
Restart submission under an inherited hook policy is unavailable because copying
that source could bypass its child-policy ceiling; use an authorized operator
context. Tenant authorization, scope lifetime, and backend errors remain in force.

The `pkg/story` service exposes a recorded restart boundary on story nodes and
can reconstruct the complete context at that boundary before a new patch is
applied. The boundary is identified by **tenant/project, source job ID, and
logical scope ID**. An unambiguous chapter ordinal remains supported. No revision token is required: later chapters do not change
the selected history prefix.

## Select a restart boundary

Fetch the story as usual:

```go
st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{
    ProjectID: tenantID,
    JobID:     sourceJobID,
})
```

Each `JobRunStoryNode` may have:

| Go field / JSON field | Meaning |
| --- | --- |
| `RestartScopeID` / `restart_scope_id` | Exact logical boundary; use for inspection and restart. |
| `RestartChapterOrdinal` / `restart_chapter_ordinal` | Physical chapter containing the scope. Compact histories can contain several scopes in one chapter. |
| `RestartRelationship` / `restart_relationship` | `exact` for the node's scope, `containing` for an enclosing scope, or `body` for a recipe's executable root body. |
| `RestartUnavailableReason` / `restart_unavailable_reason` | Explanation when no recorded recipe boundary is available. |

`st.RestartScopes` lists the available boundaries with `chapter_ordinal`,
`scope_id`, `started_at`, and, when associated with a displayed node, `title` and
`path`. Use the scope ID to join the selected node to this list.

The list also includes **Job submission**, at chapter `0`, once the job has
recorded execution after submission. It uses the same `ScopeID` and
`ChapterOrdinal` selectors as recipe scopes and is available on existing jobs,
including jobs that failed during setup before entering a recipe scope. Display
this entry from the list even though it has no associated recipe node. Treat its
scope ID as opaque, just like the other returned IDs.

Selecting Job submission retains only the original submission and its input
artifacts. Cell-context capture, root recipe-source resolution, hook-policy
capture, and recipe execution all run again. Floating recipe references can
resolve to newer content; pinned references and embedded recipe artifacts stay
as submitted. The job retains its owning cell snapshot. Ordinary recipe-scope
restarts continue to reuse their recorded setup.

Do not substitute `TaskOrdinal`. That field identifies stored task data, not a
restart boundary. Do not calculate a boundary from a node's path or subtract one
from the returned ordinal; the service handles the retained prefix internally.

Collapsed single-task operations retain both their invocation identity and their
restart association. Repeated state visits and nested scope invocations have
their own recorded boundaries. Individual task retries use their containing
recipe scope: restarting there reruns that scope, not only the chosen task retry.
Prior job attempts can be inspected and restarted through their recorded
scope IDs; they are never mapped to the current attempt by display path.

Source-resolution nodes and locations without a recorded recipe scope may have
no target. Show the unavailable reason or let the user choose a boundary from
the explicit list.

## Inspect context before patching

```go
if node.RestartScopeID == "" {
    // Present node.RestartUnavailableReason.
    return
}
scopeID := node.RestartScopeID
before, err := svc.GetRestartContext(ctx, story.GetRestartContextRequest{
    ProjectID:      tenantID,
    JobID:          sourceJobID,
    ScopeID:        scopeID,
})
```

The `RestartContext` response contains:

| Field | Contents |
| --- | --- |
| `ProjectID`, `JobID`, `ChapterOrdinal`, `ScopeID` | The exact source and boundary inspected. |
| `Job` | The base job context in the JSON shape used by `ContextPatch.Job`. |
| `Visible` | Template-visible `inputs`, `sequence`, `states`, `vars`, `outputs`, `transition`, `failure`, `scope`, `context`, `item`, and `index`. |
| `RedactedPaths` | JSON pointers to redacted values in the response, for example `/visible/sequence/write/outputs/api_key`. |

The snapshot includes unchanged values. Scoped patches address
`Visible["sequence"][id]["outputs"]` or
`Visible["states"][id]["outputs"]`. These containers contain only outputs
visible at the chosen boundary; a later task's result will not appear there.

For Job submission, inspection reads the original submission directly without
replaying execution or resolving the recipe. `Job` contains the submitted job
context and `Visible.inputs` contains the submitted recipe inputs, with the same
redaction policy. Recipe defaults, setup-derived context, and scoped outputs do
not exist yet. This snapshot is read-only: submission restarts accept an absent
or empty patch and reject nonempty context patches. Choose a recipe scope when
you need to patch execution context.

The context is captured at scope entry, before that scope resolves its own
inputs or runs its body. Thus `Visible.inputs` belongs to the resolver being
patched and need not equal the displayed operation's resolved inputs.

`Visible` also contains read-only information. Only the existing patch contract
is editable: job-context patches and local sequence/state **outputs**. Workspace
and cell-resolution identities cannot be patched. Artifacts and objects remain
references; inspection does not fetch their content. Runtime paths and other
execution-specific values may remain symbolic, and transient resolver metadata
such as `scope.timestamp` is reconstructed during replay.

Sensitive values use the existing redaction policy. Treat `RedactedPaths` as
metadata and never submit redaction placeholders back as values. Missing or
unreconstructable context is an error, not an empty successful snapshot.

## Submit the restart

Submit the selected boundary and only the changes the user requested:

```go
patch := &task.ContextPatch{
    Scopes: []task.ScopePatch{{
        Container: "sequence",
        ID:        "write",
        Outputs:   map[string]any{"document_path": "design.md"},
    }},
}
result, err := svc.RestartRecipeJob(ctx, story.RestartRecipeJobRequest{
    ProjectID:      tenantID,
    JobID:          sourceJobID,
    RequestID:      "recovery-1", // Fresh ID for a changed selection or payload.
    ScopeID:        scopeID,
    Patch:         patch,
})
// result.JobID identifies the new continuation job.
```

The examples use `github.com/colony-2/c2j/pkg/story` and
`github.com/colony-2/c2j/pkg/task`. Construct the service with the same engine and
schema registry configuration used for existing story/restart calls.

Both requests accept `ScopeID`. Supply either `ChapterOrdinal` or `ScopeID`,
never both. A chapter containing multiple restartable scopes is rejected as
ambiguous; use the returned logical scope ID to select the intended visit.
An absent patch means an unpatched restart. Map nulls and arrays retain the
existing patch semantics: map nulls delete keys and arrays replace values.

Restart creates a new job and retains the recorded prefix before the selected
scope. It does not rewind or cancel the source job. Previously patched work in
that retained prefix is reflected in inspection. A new restart **at the same
boundary** replaces that boundary's previous patch; the before-state is the
base for the new patch, not the old patched invocation. Restarting without a
patch at that boundary therefore does not retain its previous patch.

A submission restart instead retains chapter `0` itself and discards all later
execution, including previous context patches. Goal selection and request-ID
idempotency use the existing restart behavior.

## Read-only behavior and errors

Inspection reuses c2j's interpreter and JobDB's read-only replay, stopping before
the selected scope executes. It does not execute tasks, consume external input,
submit jobs, create restart records, or modify stored history. Expression
extension functions may be reevaluated; they are assumed to have no side effects.
Inspection can fail if the history or recipe artifacts needed to reach the
boundary are unavailable or incompatible with replay.

Use `errors.Is` with these public errors:

| Error | Meaning |
| --- | --- |
| `story.ErrInvalidRestartBoundary` | The target is neither an available submission boundary nor a recorded patchable scope, or both target selectors were supplied. |
| `story.ErrRestartContextUnavailable` | The selected boundary exists but its context could not be reconstructed. The underlying replay error is preserved. |
| `story.ErrNotFound` | The source job is not found in the requested tenant/project. |
| `story.ErrInvalidProject` | The tenant/project was omitted. |

Caller cancellation and deadlines are returned normally. Use the source job ID
for the ordinal; after creating a continuation, fetch its own story before
choosing a boundary in that new job.

## Dialog behavior

1. Initialize selection from the clicked node's restart ordinal on every open.
2. When patching is enabled, fetch and display **Context before patching**.
3. Keep that snapshot separate from **Patch payload**; editing the patch does not
   change the before-state.
4. Refetch when the boundary changes and ignore responses for older selections.
5. Show loading/errors explicitly and preserve the user's patch edits.
6. Submit only the patch, never the complete snapshot.

After-state preview is not part of this API.
