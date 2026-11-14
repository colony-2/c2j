# Legacy absent-hook histories

`sequence.db.gz` and `state.db.gz` are complete disposable SQLite JobDB histories,
including inline artifact bytes. They were generated with the unmodified compiler
and worker from c2j `d031eb49c6777555cca1f58449b8451b1fc77c37`, using
`TestGenerateAbsentHookLegacyFixtures` in `hook_absent_workspace_replay_test.go`.
The JSON files contain the corresponding job key and expected outputs. Source
repositories were temporary and are gone. There are no credentials in these files.

Each recipe executes a local publication sentinel, confirms hook absence, enters
the include-local workspace, and then enters another workspace. The direct-state
fixture places the hook on a state, within the same outer sequence. Offline replay
checks the original outputs, caller Git context, and later workspace scope ID.

To regenerate from this checkout without changing its source files, build an
old-compiler binary with Go's source overlay. The decision tests reference new
symbols, so the overlay replaces that one test file with an empty package:

```sh
scratch=$(mktemp -d)
git show d031eb49c6777555cca1f58449b8451b1fc77c37:pkg/worker/compiler/hooks.go > "$scratch/hooks.go"
printf 'package compiler\n' > "$scratch/empty_test.go"
python3 - "$PWD" "$scratch" <<'PYTHON'
import json, os, sys
repo, scratch = sys.argv[1:]
base = os.path.join(repo, 'pkg/worker/compiler')
with open(os.path.join(scratch, 'overlay.json'), 'w') as f:
    json.dump({'Replace': {
        os.path.join(base, 'hooks.go'): os.path.join(scratch, 'hooks.go'),
        os.path.join(base, 'hook_absent_workspace_decision_test.go'):
            os.path.join(scratch, 'empty_test.go'),
    }}, f)
PYTHON
go test -overlay "$scratch/overlay.json" -c -o "$scratch/legacy.test" ./pkg/worker/compiler
cd pkg/worker/compiler
C2J_GENERATE_ABSENT_HOOK_FIXTURES="$PWD/testdata/absent-hook-workspace" \
  "$scratch/legacy.test" -test.run '^TestGenerateAbsentHookLegacyFixtures$' -test.v
```

Use the verify hook's environment isolation when running from a c2j task. The
generator rejects histories containing the new decision field. Do not regenerate
these fixtures with the corrected compiler and an invented old result.

## Interrupted histories

`interrupted/{sequence,state}-{load,workspace}.db.gz` were produced by the same
unmodified `d031eb49c6777555cca1f58449b8451b1fc77c37` compiler and worker, using
`TestGenerateAbsentHookLegacyInterruptedFixtures`. Each database is closed and
copied immediately after committing either the absent load result or the
include-workspace resolution result, before the compiler consumes that outcome.
The old compiler then resumes a separate copy to supply the expected outputs in
the adjacent JSON. Its publication sentinel runs exactly once across that resume.

Each adjacent Git bundle contains the disposable source repository, with no
credentials. The test restores it to a temporary path and maps its recorded
canonical repository identity through the embedding's repository transport. It
does not rewrite any durable command, input, result, ownership, or history. The
source's branch tip gains a present hook before the candidate resumes; retained
old load results must still select absence. Only the later floating workspace's
HEAD follows that new tip; all fallback context and workspace scope IDs must
match the old compiler's oracle.

`TestHookAbsentLegacyInterruptedResumeAndRestart` checks both interrupted
frontiers for sequence and direct-state hooks, then supported retained-prefix
restarts at the hook wrapper and later operation. It checks legacy empty fallback
inputs, include-workspace cell/Git/scope context, two retained hook-resolution
tasks, both workspace entries, identical later workspace identity, and no further
publication activity. Completed replay after each restart has no task workers
and leaves chapters unchanged. The fixture generator is explicitly gated; the
regression itself runs in the ordinary compiler suite without an old binary or
live service.

To regenerate only these interrupted fixtures, use the overlay build above, then
run the binary from `pkg/worker/compiler` with the same output directory:

```sh
C2J_GENERATE_ABSENT_HOOK_FIXTURES="$PWD/testdata/absent-hook-workspace" \
  "$scratch/legacy.test" -test.run '^TestGenerateAbsentHookLegacyInterruptedFixtures$' -test.v
```
