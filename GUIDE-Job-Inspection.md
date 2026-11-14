# Inspect jobs and recover saved work

Use `c2j job` to inspect persisted jobs, execution scopes, task outputs, artifacts,
and cumulative Git snapshots. Inspection commands do not run or replay recipes.
The separate operator action `c2j job clear-crash-concern JOB` resets a blocked
job's crash counter. See [crash-concern recovery](GUIDE-Cortex-RecipeJob-API.md#clear-crash-concern)
for its credentials, behavior, and Go API.

## Find the relevant work

```sh
c2j job list --json
c2j job list --parent JOB --json
c2j job show JOB
c2j job log JOB
c2j job story JOB --json
```

`show` includes `job.completion_status` and the exact persisted
`job.completion_detail` when present. The same detail field carries failure
diagnostics and cancellation feedback, including messages supplied while a job
was waiting for a review. The text view prints it under `Completion detail:`.
No final recipe output or completed review response is required. Empty detail
is omitted; c2j does not invent a cancellation reason. For large messages, use
`c2j job show JOB --output job.json` to write the complete JSON response.

Inside an ordinary task, `self` identifies the current job. `c2j job list` lists
backend-visible jobs in the selected tenant, and `c2j job log self` inspects its
own stored history. `c2j self` advertises `jobs.inspect`. Reads use the
authenticated task helper and are available independently of permission to submit
new jobs. They follow existing JobDB read permissions for the lifetime of the
task, including sibling, unrelated, and terminal jobs. Use
`c2j job list --parent JOB` to select only that job's direct children. Outside a
task, the commands use configured JobDB access. `--jobdb` and `--embed` select a
standalone destination; neither bypasses task authority.

`log` lists native task occurrence IDs. Copy one exactly, quote it in the shell,
and use `--task "$TASK"`. Repeated nodes have different IDs. A task's `attempts`
list identifies retries; `--attempt N` selects one, otherwise the latest is used.
Chapter ordinals and node names are not task selectors.

`story` lists recorded execution scopes, without loading
recipe source. It is a persisted execution view, not the reconstructed UI story.
Compact history can omit normal scope completion; those rows say
`completion_not_recorded` rather than claiming the scope is still running.
Running jobs can have work in process that has not yet been recorded; inspection
reports this limit and does not invent temporary task IDs.

## See a job's submission family

```sh
c2j job dag JOB
c2j job dag self --json
c2j job dag JOB --subtree --max-nodes 100 --max-depth 8 --timeout 10s
c2j job dag JOB --json --output family.json
```

`dag` follows recorded runtime parents to the highest readable ancestor, then
shows its descendants across cells and job types, including siblings and cousins.
Each edge means **parent submitted child**. Shared goals, waits, recipe metadata
and copied restart histories do not create edges. `--subtree` skips parent reads
and shows only the selected job and its descendants. Hidden and archived jobs
are requested under existing JobDB permissions, within the selected tenant.

Text gives each job one line: a status symbol, title and full ID aligned in a
column on the right. Tree connectors show the submission path, and `[selected]`
marks the requested job. For example, selecting the implementation job includes
its parent, sibling and descendant:

```text
▶ Build application                        parent-full-id
|-- ▶ Implement DAG  [selected]             selected-full-id
|   `-- ✓ Explain submission relationships  descendant-full-id
`-- ○ Check documentation                  sibling-full-id
```

Symbols are `▶` active, `○` ready, `…` waiting, `✓` succeeded, `✗` failed,
`⊘` cancelled, `!` cancellation requested or scheduler concern (expired lease
or crash concern), and `?` unknown status or completion outcome.
Titles escape terminal controls and shorten to at most 72 displayed characters;
JSON keeps the full title. Missing titles say `(untitled job)`.
Siblings sort by full ID. Healthy trees have no metadata footer or warning.
An incomplete or inconsistent tree adds a warning with affected jobs and reasons;
full diagnostic detail remains in JSON. A failed job alone does not mean the tree
is inconsistent.
The result observes an interval rather than an atomic snapshot; rerun to refresh.

For agents, **use `c2j job dag JOB --json`** instead of parsing the text tree.
`--json` writes only the structured document to stdout; diagnostics go to stderr.
Use `--json --output family.json` to save it without a terminal byte cutoff.
The same family is returned in both formats, including parents and descendants.

Defaults are 1,000 retained jobs, 64 descendant levels and 30 seconds. Positive
`--max-nodes`, `--max-depth` and `--timeout` values override those limits. Fixed
safety caps are 64 ancestor hops, 200 child pages and 300 inspection requests.
Depth starts at the highest retained ancestor, even when selecting a deep child.
The selected job and readable ancestor chain remain visible at depth boundaries.

Exit 0 means the requested observed listings exhausted. Exit 2 means usable
partial output was written: read the text warning or inspect JSON ancestry, child
boundaries and anomalies for unreadable branches, limits, repeated cursors or
inconsistent relationships.
An unqueried listing never implies a leaf. A confirmed root can still have
incomplete descendants. Exit 1 means invalid input, entry/protocol failure or
output failure. A successful empty lookup only means unavailable under current
visibility; it does not prove deletion or denial.

DAG JSON is a separate version-1 document with `selected`, `scope`, `nodes`,
typed submission `edges`, `root`, `observed`, `limits`, `counts` and `coverage`.
Coverage includes ancestry state, a child-listing state for every retained node,
anomalies and `complete`. Ancestry in subtree mode is `not_requested`, with
`root.confirmed` false. `limits.timeout_ns` is the effective duration in
nanoseconds. Empty collections are arrays, and opaque next cursors are preserved.

| JSON field | How to use it |
| --- | --- |
| `version` | Check the document contract; currently `1`. |
| `selected.tenant`, `selected.id` | Identify the requested job independently of node order. |
| `nodes[]` | Read full `id`, recorded `parent`, `cell`, `title`, `status`, `completion_status` and other job context. Optional labels and parent are omitted when empty; absent or empty completion status means unknown. |
| `edges[]` | Follow accepted `{type: "submission", parent, child}` connections. Node metadata alone does not establish an accepted edge. |
| `root.id`, `root.confirmed` | Locate the highest retained ancestor and check whether its parentlessness was confirmed. |
| `coverage.complete` | Check whether the requested observed listings exhausted. |
| `coverage.ancestry`, `coverage.children[]`, `coverage.anomalies[]` | Inspect unfinished reads and rejected relationships. Child entries use `id`, `state`, `reason`, `next_cursor`. |
| `observed`, `limits`, `counts` | Read observation times, effective bounds and actual work performed. |

For example, after saving `family.json`, use jq to select job summaries or
inspect coverage. Exit 2 from c2j still leaves a usable document; inspect it
before treating the family as complete. Avoid a shell `&&` chain that would
skip reading that partial result.

```sh
jq '.nodes[] | {id, parent, cell, title, status, completion_status}' family.json
jq '.edges[] | {parent, child}' family.json
jq '{complete: .coverage.complete, ancestry: .coverage.ancestry,
     unfinished: [.coverage.children[] | select(.state != "exhausted")],
     anomalies: .coverage.anomalies}' family.json
```

`--output FILE` creates a new file in the selected text or JSON format;
`--output -` writes stdout. Existing files remain untouched. Usable partial files
survive exit 2; failed writes or closes remove newly created files. DAG JSON has
no terminal byte cutoff. Inside tasks, this command requires workers supporting
the family inspection protocol; upgrade the worker and CLI together. It uses the
authenticated inspection helper even with `--jobdb`, without ambient fallback.
Compact reads do not load job execution histories, outputs, artifacts or waits.

## Read exact outputs and files

```sh
c2j job log JOB --task "$TASK" --json
c2j job fields JOB --task "$TASK" --limit 25 --json
c2j job log JOB --task "$TASK" --field /response/summary
c2j job log JOB --task "$TASK" --field '' --output task-output.json

c2j job result JOB --json
c2j job fields JOB --json
c2j job result JOB --field /summary --output summary.json

c2j job artifacts JOB --task "$TASK" --json
c2j job artifact JOB --task "$TASK" --name check.log --output check.log
```

Omit `--task` from artifact commands to select job-result artifacts. An exact
stored key from `reference.stored.key` can be passed as JSON with `--ref` instead
of `--name`. A missing output differs from JSON `null`, `false`, `0`, and `""`.
`--field` uses JSON Pointer syntax: `/` within a key is `~1`, and `~` is `~0`.
Empty `--field ''` selects the entire output, preserving numbers and types.

Discovery pages default to 25 entries, with a maximum of 100. Continue using the
returned `--cursor`. Task/result summaries cap their field and artifact inventory;
use `fields` and `artifacts` for paginated discovery. Previews mark truncation.
Large exact values require `--output FILE`; terminal JSON for these commands is
limited to 256 KiB (the separate DAG document has no byte cutoff).
Files are created exclusively, and a failed download removes its partial file.
`--output -` explicitly streams to stdout, including binary artifact bytes.

Failed tasks retain available outputs and diagnostics even when the job never
produced a final recipe result. Artifact inventories distinguish stored, inline,
external, and unavailable data. External URLs are not automatically fetched.
Stored bytes may expire after discovery; a failed read is reported as an error.

## Export a cumulative snapshot in one command

```sh
c2j job snapshot JOB --task "$TASK" --dir ./recovered
git -C ./recovered/worktree status
git -C ./recovered/worktree diff refs/c2j/base HEAD
```

The destination must not exist. For one workspace, the result is:

```text
recovered/
  snapshot.json
  worktree/                 # Git checkout already at the saved commit
  artifacts/<producer>/attempt-N/<original-name>
  bundles/<workspace>.bundle
```

`snapshot.json` records job/task/attempt provenance, repository and cell, workspace
scope, full base and target hashes, bundle prerequisites, and local file paths.
It is written last. Failed exports remove the directory they created.
Existing directories are never overwritten.

The snapshot contains **cumulative Git state through that point**, plus retained
task artifacts through that boundary. It is not limited to changes made by the
selected task. Artifacts from failed attempts remain useful evidence; identical
filenames from different producers or attempts are kept separately. Snapshot
bundles and automatic diff artifacts are represented through the Git export.
Final job-result artifacts are included when no task boundary is selected.

With several workspaces, checkouts live under `workspaces/`; their exact paths are
printed and recorded in `snapshot.json`. Each scope keeps its own repository and
Git state, including hook workspaces. Select a scope or cell when useful:

```sh
c2j job snapshot JOB --task "$TASK" --workspace SCOPE_ID --dir ./selected
c2j job snapshot JOB --task "$TASK" --boundary before --dir ./incoming
c2j job snapshot JOB --dir ./latest
c2j job snapshot JOB --task "$TASK" --json
```

The default boundary is `after`. An explicitly selected failed attempt can export
its saved diagnostic Git state, labeled `failed_attempt`. Without `--task`, the
export uses the latest successful task state in each workspace. It does not adopt
a failed attempt's Git state. `before` selects the incoming workspace and requires
`--task`. A task that made no changes can reuse an earlier bundle.

The internal “thin pack” is a Git bundle. Export downloads it, fetches its exact
prerequisites using your existing Git credentials, verifies it, imports its
objects, and checks out the recorded target. There is no push or change to an
existing checkout. An empty-base snapshot needs no base fetch. Existing snapshots
whose exact hashes or bytes were not retained report unavailable; they never
substitute the current branch tip. Older floating, unchanged tasks may lack an
exact recorded checkout. Upgrade workers to record before/after observations.

If a required artifact is external or missing, a complete directory export fails
explicitly. Individual artifact reads and snapshot metadata remain available.

## Reuse the changes

The exported checkout is ready for editing and testing. To move just the cumulative
changes to another checkout, standard Git commands are sufficient:

```sh
git -C ./recovered/worktree diff --binary refs/c2j/base HEAD > changes.patch
git -C ./destination apply --3way /absolute/path/to/changes.patch
```

The target checkout must contain the appropriate base objects for three-way
application. Review and resolve conflicts normally. `refs/c2j/snapshot` names the
saved head; `refs/c2j/base` names the base when one exists. Empty-base snapshots
have no base ref and can be used directly as their own checkout.

Supply selected files using existing `c2j submit --artifact name=path` flags or
native artifact references. Attachment alone does not install Git changes or
bind every file into a recipe's inbox. Recovery remains an ordinary new job.

## Programmatic interface

`pkg/jobinspect.API` provides `Inspect(context.Context, Request)` and
`Download(context.Context, Request)`. `Service` projects JobDB's native execution
records; it does not need a recipe provider or live worker. `Export` performs the
directory workflow against either the service or authenticated task transport.
The wire/result schema has `version: 1`.

The task helper exposes these same operations and streams artifact responses.
The current JobDB client still buffers remote artifacts internally and loads
history before projection; pagination bounds the returned inventory, not the
backend history read. No additional JobDB API is required by this implementation.
