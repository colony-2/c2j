# Submission text histories

The two compressed SQLite databases were generated with the unchanged evaluator
at `0c78eddacdd74eab37582ed95668cb359d970a11`. The adjacent JSON files identify
the jobs and the successful output. They contain disposable jobs and no secrets.

Both submissions contain multiline Unicode title and prompt. One recipe declares
both; its sentinel activity ran once. The other declares neither; the old evaluator
rejected it before the sentinel. The sentinel uses a local task worker, without
Git or external services. Replay has no task workers or live recipe sources.

`TestRecipeSubmissionInputsHistoricalReplayAndRestart` replays the success and
checks that replaying the old failure reports missing history without writes.
A submission restart then completes the old failure using the candidate evaluator.
It preserves the original submission and leaves the original failed job unchanged.

To regenerate, use a source overlay with the baseline version of
`pkg/worker/compiler/recipe_inputs.go` and run the opt-in generator. The generator
checks the original failure and zero activity calls; it refuses candidate behavior.

```sh
scratch=$(mktemp -d)
git show 0c78eddacdd74eab37582ed95668cb359d970a11:pkg/worker/compiler/recipe_inputs.go > "$scratch/recipe_inputs.go"
python3 - "$PWD" "$scratch" <<'PY'
import json, os, sys
repo, scratch = sys.argv[1:]
with open(os.path.join(scratch, 'overlay.json'), 'w') as f:
    json.dump({'Replace': {
        os.path.join(repo, 'pkg/worker/compiler/recipe_inputs.go'):
            os.path.join(scratch, 'recipe_inputs.go'),
    }}, f)
PY
C2J_GENERATE_SUBMISSION_INPUT_FIXTURES="$PWD/pkg/worker/compiler/testdata/recipe-submission-inputs" \
  go test -overlay "$scratch/overlay.json" ./pkg/worker/compiler \
  -run '^TestGenerateRecipeSubmissionInputsLegacyFixtures$' -count=1 -v
```

When running within a c2j task, unset live job identity variables as the repository
verify hook does. The baseline uses generic `Apply`, so it does not require an
overlay of the new recipe helper.
