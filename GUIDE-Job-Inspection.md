# Inspect jobs and recover saved work

Use `c2j job` to inspect persisted jobs, execution scopes, task outputs, artifacts,
and cumulative Git snapshots. These commands do not run or replay recipes.

## Find the relevant work

```sh
c2j job list --json
c2j job list --parent JOB --json
c2j job show JOB
c2j job log JOB
c2j job story JOB --json
```

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
Large exact values require `--output FILE`; terminal JSON is limited to 256 KiB.
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
