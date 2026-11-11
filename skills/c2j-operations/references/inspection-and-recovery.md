# Inspect before repeating work

Run `c2j self --json` and `c2j cells` to discover the actual connection, workspace and permissions. Task inspection uses the authenticated helper independently of submission permission; an explicit `--jobdb` cannot bypass it. Missing optional metadata is not a denial. Outside tasks, use configured JobDB access. These commands read persisted records without running or replaying the job.

Follow a known producer job reference before concluding that an absent inbox file is lost. Stop discovery once the evidence answers the question:

```sh
JOB=self
c2j job list --json
c2j job list --parent "$JOB" --json
c2j job show "$JOB" --json
c2j job log "$JOB" --json
```

`show` includes persisted completion detail, including cancellation feedback, even without a final result. Copy an exact native task occurrence ID into `TASK`, quoted in the shell; a node name or chapter number is not a task ID. Select by path, status, error and attempt. Use `--attempt N` consistently on all related reads for a retry; otherwise reads select the latest attempt.

```sh
c2j job log "$JOB" --task "$TASK" --json
c2j job fields "$JOB" --task "$TASK" --limit 25 --json
# Copy a returned pointer into FIELD; do not guess a response shape.
c2j job log "$JOB" --task "$TASK" --field "$FIELD" --output selected-value.json
c2j job log "$JOB" --task "$TASK" --field '' --output task-output.json
c2j job artifacts "$JOB" --task "$TASK" --json
# Copy an exact returned name into NAME.
c2j job artifact "$JOB" --task "$TASK" --name "$NAME" --output check.log
c2j job result "$JOB" --json
c2j job fields "$JOB" --json
c2j job result "$JOB" --field "$FIELD" --output result-value.json
```

Follow `next_cursor` using `--cursor` on the same inventory command. Previews can be truncated; inventories describe types and sizes, not exact contents. JSON Pointers address the task output, not the inspection envelope. A string-valued `/response` must be downloaded and parsed locally before inspecting its interior. Missing differs from null, false and empty. Artifact commands without `--task` address final-result artifacts, not all task files. Failed/running jobs may have no final result: inspect their failed task and earlier producers instead.

Use fresh output paths in scratch outside checkout/outbox; downloads refuse overwrite. Preserve actual denied, expired-byte, external-artifact and unavailable-snapshot diagnostics. Access failure is not evidence of absence; do not ask another child to bypass the same restriction. Report the exact unavailable item and consequence if essential.

# Recover selected changes

```sh
# First enter scratch outside checkout/outbox; recovered must not exist.
c2j job snapshot "$JOB" --task "$TASK" --dir ./recovered
```

Read `snapshot.json` for repository, workspace scope, base/target hashes and attempt provenance. A snapshot is cumulative through the boundary, not just that task's edits. Select `--workspace SCOPE_ID` for multiple workspaces. `--boundary before` requires a task; default is after. Without a task, export uses latest successful state, not a failed attempt's Git state. Explicit failed attempts can expose diagnostic state. Exact missing hashes/bytes never fall back to today's branch tip. Export leaves the active checkout unchanged.

For one workspace with a recorded base:

```sh
git -C ./recovered/worktree diff --binary refs/c2j/base HEAD > changes.patch
```

Check the base first: new repositories may have no `refs/c2j/base`. Review and select useful files/patches, document producer/base provenance, apply in the intended checkout and retest. Do not blindly adopt all cumulative changes. Keep the original assignment, design and acceptance with recovered work; preserve the receiver's handoff. Artifact attachment alone neither applies a patch nor binds every file into an inbox. If normal resolution requires a published revision, arrange publication; an unpublished hash is not remotely fetchable merely because it exists in a snapshot.

# Runtime restart is a different operation

Discover installed syntax with `c2j job --help`, `c2j restart --help`, `c2j job restart-points --help` and `c2j job restart-context --help`.

```sh
c2j job restart-points "$JOB" --json
c2j job restart-context "$JOB" --scope "$SCOPE" --output before.json
# Only when the requested continuation is authorized:
c2j restart "$JOB" --scope "$SCOPE" --request-id recovery-1 --patch-file patch.json
```

Copy an exact recorded scope; do not derive it from task ordinals or display paths. Use exactly one scope or chapter selector; chapter 0 selects submission and accepts only absent/empty patches. Ambiguous chapters require a scope. Context inspection reconstructs the boundary without executing tasks; it may fail when required history/source is unavailable.

Current source accepts ordered RFC 6902 arrays, for example `[{"op":"remove","path":"/sequence/write/outputs/obsolete"}]`. Explicit null writes null; only remove deletes. Editable roots are `/context`, `/sequence`, `/states`, limited to permitted typed context fields and visible outputs. Inputs, workspace identities and runtime metadata are read-only. Never copy a full snapshot or redaction placeholders into a patch. Use a fresh request ID for changed selection/payload; reuse only for identical retries. Older binaries/helpers with object merge-patch contracts require coordinated upgrades, not format fallback.

Restart creates a continuation job; the source stays unchanged and uncancelled. Selected work and external effects can repeat. Submission identity does not prove completion. A new adaptive build using `replaces`/`resume_from` instead follows [adaptive policy](../../c2j-recipes/references/adaptive-workflows.md); restart does not supply native design approval.

Mechanics: [inspection guide](https://github.com/colony-2/c2j/blob/a34601101b53c1ef7bef2bea46acbddd537597e3/GUIDE-Job-Inspection.md), [restart contract](https://github.com/colony-2/c2j/blob/a34601101b53c1ef7bef2bea46acbddd537597e3/GUIDE-Restart-Context.md). Examples describe this reviewed source; check your installed binary/helper before use.
