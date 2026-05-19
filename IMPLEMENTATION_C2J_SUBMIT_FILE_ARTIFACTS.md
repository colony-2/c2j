# C2J Submit File Artifacts Implementation Spec

Status: draft

## Context

`c2j submit` currently accepts recipe inputs through `--inputs-json`,
`--inputs-file`, and the positional prompt shortcut. It does not expose a CLI
way to attach local files, such as markdown documents, as job artifacts.

The lower-level runtime already has most of the artifact plumbing:

- `workflowctl.StartJob` has `Artifacts []swf.Artifact` and
  `ArtifactRefs []artifacts.Ref`.
- `starter.StartRecipeJobWithOptions` appends `startJob.Artifacts` to the SWF
  task data.
- The recipe compiler can bind artifact refs into op inputs through an op
  node's `artifacts:` map.
- The op executor materializes bound artifacts into the op inbox.

The missing pieces are the submit CLI surface and a stable template namespace
that lets recipe authors refer to artifacts attached at job submission time.

## Goals

1. Allow users to attach one or more local files when submitting a job.
2. Store attached files as normal SWF artifacts on the submitted recipe job.
3. Expose submitted artifact refs to recipe templates in a predictable
   namespace.
4. Let recipes explicitly bind submitted artifacts into only the ops that need
   them.
5. Preserve existing submit behavior when no artifact flags are provided.

## Non-Goals

- Do not auto-inject submitted files into every op. Some ops do not accept
  artifacts, and automatic injection would leak files across unrelated steps.
- Do not add directory, glob, archive expansion, or HTTP upload support in the
  first implementation.
- Do not encode file contents inside recipe inputs.
- Do not change existing output artifact capture semantics.

## User Experience

Add a repeatable `--artifact` flag to `c2j submit`.

```bash
c2j submit \
  --recipe-file ./recipes/review-docs.yaml \
  --artifact ./docs/brief.md \
  --artifact requirements=./docs/requirements.md \
  --run \
  --embed
```

Accepted forms:

```text
--artifact <path>
--artifact <name>=<path>
```

`<path>` is the local source file. Relative paths resolve against the submit
working directory.

`<name>` is the submitted artifact name. When omitted, c2j uses
`filepath.Base(path)`.

Example recipe usage:

```yaml
id: review-docs
version: "1.0"
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
      requirements.md: '${{ context.artifacts["requirements"] }}'
    inputs:
      run: |
        test -f "${{ context.environment.op.inbox }}/brief.md"
        test -f "${{ context.environment.op.inbox }}/requirements.md"
        cat "${{ context.environment.op.inbox }}/brief.md"
```

The `artifacts:` map key controls the path under the op inbox. The
`context.artifacts[...]` lookup selects the submitted artifact by its submitted
name.

## CLI Contract

Add to `cmd/c2j/internal/cmd/job_submit.go`:

```go
flags.StringArrayVar(
    &opts.ArtifactSpecs,
    "artifact",
    nil,
    "Attach a local file as a job artifact; repeatable, accepts PATH or NAME=PATH",
)
```

Use `StringArrayVar`, not `StringSliceVar`, so names and paths containing commas
are not split by cobra.

Add to `cmd/c2j/internal/submitjob.Options`:

```go
ArtifactSpecs []string
```

Validation rules:

- Empty specs are invalid.
- Specs containing `=` use the first `=` as the `NAME=PATH` separator.
- Source paths must exist and be regular files.
- Directories are invalid in phase 1.
- Artifact names must be relative, slash-separated paths.
- Artifact names must not be empty, absolute, contain `..`, contain a Windows
  drive prefix, contain `=`, or contain empty path segments.
- Duplicate artifact names are invalid.
- For local embedded recipes, reject an attached artifact whose name exactly
  equals the internal recipe artifact name:
  `<recipe-id>.recipe.yaml`.

The source file path may be absolute or relative. Only the artifact name is
restricted to a portable relative path.

## Submit Data Flow

Add a helper in `cmd/c2j/internal/submitjob`, for example
`loadSubmitArtifacts(opts Options, recipeName string) ([]swf.Artifact, error)`.

The helper should:

1. Parse each `--artifact` spec into `{Name, SourcePath}`.
2. Resolve source paths relative to `opts.WorkingDir`.
3. Validate the source file and artifact name.
4. Read file bytes.
5. Build `swf.NewArtifactFromBytes(name, data)` for each file.

Then pass the loaded artifacts into `workflowctl.StartJob`:

```go
start := workflowctl.StartJob{
    TenantId:   opts.TenantID,
    RecipeName: recipeName,
    Inputs:     inputs,
    Artifacts:  submitArtifacts,
    ...
}
```

`starter.StartRecipeJobWithOptions` already forwards `startJob.Artifacts` into
the SWF task data, so this should not require changes in `pkg/starter`.

## Template Namespace

Expose submitted artifact refs under:

```text
context.artifacts
```

Example:

```yaml
artifacts:
  design.md: '${{ context.artifacts["design-doc"] }}'
```

Implementation changes:

1. Add artifact refs to `pkg/contextual.JobContext`:

   ```go
   Artifacts map[string]artifacts.Ref `json:"artifacts,omitempty"`
   ```

2. Add the same field to `pkg/contextual.TaskExecutionContext`.

3. Update `contextual.NewTaskExecutionContext` to copy
   `JobContext.Artifacts`.

4. Update `TaskExecutionContext.JobContext()` to preserve artifacts.

5. Update `pkg/template/template_interpolate.go` so
   `goTemplateContextMap()` includes:

   ```go
   "artifacts": ctx.Artifacts,
   ```

6. Ensure CEL can evaluate `context.artifacts["name"]` by keeping the
   `TaskExecutionContext` native type registered and by using the existing
   `artifacts.Ref` native type registration.

Do not use `inputs.artifacts`. That would collide with recipe input schemas and
would make submitted files look like user-authored scalar input data instead of
runtime artifact refs.

## Job Worker Artifact Ref Construction

The root job payload is serialized before SWF assigns durable artifact keys, so
the submit path cannot reliably precompute `ArtifactRefs` for newly attached
local files.

Instead, construct the submitted artifact namespace inside
`pkg/worker/compiler/job_worker.go` after:

```go
artifacts, err := jobData.GetArtifacts()
```

Build a map of submitted artifact refs from the artifacts present on the recipe
job:

1. Exclude the internal embedded recipe artifact whose name is
   `<input.RecipeName>.recipe.yaml`.
2. Convert remaining artifacts with `recipeartifacts.RefFromArtifact`.
3. Merge any refs already present in `input.ArtifactRefs`.
4. Error on duplicate submitted artifact names with different identities.
5. Store the final map on `runContext.Artifacts` before calling
   `ExecuteRecipeWithExecutor`.

This also makes submitted artifacts from child recipe jobs visible when a parent
recipe starts a child with explicit artifacts.

## Recipe Authoring Contract

Recipe authors explicitly opt into using submit-time files by binding them into
an op's `artifacts:` map:

```yaml
sequence:
  - id: read_doc
    op: command_execution
    artifacts:
      doc.md: '${{ context.artifacts["doc.md"] }}'
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/doc.md"'
```

If an op does not accept artifacts and the recipe declares `artifacts:`, the
existing compiler validation should continue to fail with:

```text
operation <op> does not accept artifacts
```

If the user did not attach the requested artifact, template resolution should
fail naturally instead of materializing an empty file.

## Edge Cases

- Multiple files with the same basename require explicit names:

  ```bash
  c2j submit --artifact api=./docs/api/README.md --artifact ui=./docs/ui/README.md
  ```

- A file named `docs/brief.md` submitted without a custom name is exposed as
  `context.artifacts["brief.md"]`, not `context.artifacts["docs/brief.md"]`.

- A custom artifact name may include safe relative subpaths:

  ```bash
  c2j submit --artifact docs/brief.md=./brief.md
  ```

  The recipe can then bind it with:

  ```yaml
  artifacts:
    brief.md: '${{ context.artifacts["docs/brief.md"] }}'
  ```

- Source paths are read at submit time. Later changes to the local file do not
  affect the submitted job.

## Tests

Add focused tests for the CLI/service layer:

- `PATH` defaults the artifact name to `filepath.Base(PATH)`.
- `NAME=PATH` preserves the custom name.
- Relative source paths resolve against `WorkingDir`.
- Missing file, directory source, invalid name, and duplicate name return clear
  errors.
- `submitjob.Run` sends attached artifacts to the fake SWF engine with expected
  names and bytes.

Add compiler/job worker tests:

- A job submitted with attached artifacts exposes
  `context.artifacts["name"]` to templates.
- A recipe can bind a submitted markdown artifact into a `command_execution`
  op and the op can read it from the inbox.
- The internal `<recipe-id>.recipe.yaml` artifact is not exposed through
  `context.artifacts`.
- Existing jobs without submitted artifacts still have an empty/nil artifact
  namespace and continue to pass.

Add integration coverage for embedded submit:

```bash
c2j submit \
  --recipe-file ./testdata/consume-submitted-artifact.yaml \
  --artifact ./testdata/brief.md \
  --run \
  --embed
```

The recipe should assert the markdown file exists in the op inbox and return a
small output derived from its contents.

## Implementation Order

1. Add `--artifact` parsing and artifact loading in `cmd/c2j/internal/submitjob`.
2. Pass loaded artifacts into `workflowctl.StartJob.Artifacts`.
3. Add `context.artifacts` fields to contextual types and template rendering.
4. Build submitted artifact refs in the recipe job worker from job input
   artifacts.
5. Add unit tests for parsing/loading.
6. Add job worker/compiler coverage for template binding and inbox
   materialization.
7. Add README examples after the behavior is covered.

## Backward Compatibility

This is additive. Existing commands, recipes, and input files behave the same
unless one or more `--artifact` flags are provided.

The new `context.artifacts` field is empty when no submitted artifacts exist.
It does not reserve or mutate any recipe input keys.

## Open Questions

1. Should phase 2 add directory or glob support, possibly by archiving or by
   reusing the existing external artifact expansion code?
2. Should `c2j list` or story output show submitted job artifacts separately
   from artifacts produced by recipe steps?
3. Should there be an optional per-file or total submit artifact size limit once
   the runtime's preferred artifact limits are documented?
