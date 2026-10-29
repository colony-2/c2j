# Repository-local recipe hooks

A shared recipe can call a recipe in its consuming repository with `cell://`.
The call resolves when execution reaches it. It pins the current candidate before
binding inputs, then records the hook definition (including relative includes)
or its absence. Read-only and absent hooks also pin the candidate. Existing
non-cell includes keep their eager expansion behavior.

```yaml
id: build
sequence:
  - id: project_check
    include:
      recipe: cell://.c2/recipes/hooks/check.yaml
      if_missing:
        outputs: {decision: continue, summary: No additional checks.}
    inputs:
      candidate: '${{ context.git.hash }}'
    expect_outputs:
      type: object
      required: [decision, summary]
      properties:
        decision: {enum: [continue, revise]}
        summary: {type: string}
outputs:
  decision: '${{ sequence.project_check.outputs.decision }}'
  summary: '${{ sequence.project_check.outputs.summary }}'
```

A corresponding `.c2/recipes/hooks/check.yaml` could contain:

```yaml
id: project_check
input_schema:
  candidate: {type: string, required: true}
inputs:
  candidate: '${{ inputs.candidate }}'
sequence:
  - id: checks
    op: command_execution
    inputs:
      run: ./scripts/project-checks.sh
outputs:
  decision: continue
  summary: Project checks passed.
```

State-machine calls use the same syntax; parent transitions read
`states.project_check.outputs.decision`. The hook returns results, and the parent
owns revision routes and any required revalidation or approval gates.

Only an absent ordinary file triggers `if_missing`. Invalid YAML, symlinks,
lookup failures, and invalid outputs fail the invocation. `expect_outputs` is a
self-contained JSON Schema; external schema references are rejected. The schema
is checked before hook execution and outputs are checked before publication or
parent transitions, including optional fallback and catch-continue outputs.

Pinning and loading are separate durable tasks. Resume uses the recorded pin,
source, and policy. Hook retries reuse that definition. A later visit to the
checkpoint resolves again from the then-current candidate, including edits saved
in thin-pack artifacts that have not been pushed. Relative includes use their
source recipe's repository and commit. The hook runs in the consuming workspace;
put workspace selection on a containing node, and use the consuming repository.

Static validation checks the caller's contract and uses its output shape as a
placeholder. It does not fetch a hook or predict whether it will exist at runtime.

## Permission administration

Hook permissions live exclusively in jobdb configuration. Each entry is keyed by
its exact repository-relative hook path. All fields in the recipe `perms` schema
are supported, including secret resolution, job submission, and limits. No grants
means no hook JobDB capabilities; ordinary operation defaults do not restore them.
The secret provider retains the final authorization decision.

Choose `--tenant-wide` or `--cell CELL` explicitly. `CELL` accepts a configured
cell name or repository reference. `--jobdb URI` selects the tenant; endpoint
configuration and `--embed` follow the other c2j administration commands.

```sh
c2j hooks perms add .c2/recipes/hooks/check.yaml \
  --tenant-wide --secret TEST_TOKEN --trust-candidate-code

c2j hooks perms add .c2/recipes/hooks/check.yaml \
  --cell service --submit-jobs --trust-candidate-code

c2j hooks perms remove .c2/recipes/hooks/check.yaml \
  --cell service --secret TEST_TOKEN

c2j hooks perms get .c2/recipes/hooks/check.yaml --cell service
c2j hooks perms list --all-cells
c2j hooks perms clear .c2/recipes/hooks/check.yaml --cell service
c2j hooks perms inherit .c2/recipes/hooks/check.yaml --cell service
```

| Command | Effect |
| --- | --- |
| `add HOOK --secret NAME` / `add HOOK --submit-jobs` | Add selected grants with explicit source trust. Repeat `--secret` for more names. |
| `remove HOOK --secret NAME` / `remove HOOK --submit-jobs` | Remove selected grants, retaining an explicit empty entry if necessary. |
| `set HOOK --policy-json JSON` | Replace this hook's entire permission and trust entry; `-` reads stdin. |
| `clear HOOK` | Store an explicit deny for this hook. |
| `inherit HOOK --cell CELL` | Remove the cell override, restoring tenant inheritance. |
| `get HOOK` / `list` | Show effective entries, source scope, repository, tenant, and configuration revision. |
| `list --all-cells` | Show tenant defaults and configured cell documents. |

A cell entry replaces the tenant entry for that hook. An absent override inherits;
an empty override denies. Cell `add` and `remove` first copy the effective entry
when no override exists. That copy stops tracking subsequent tenant edits.
Tenant defaults are not a mandatory ceiling. Job-wide and callsite limits still
apply. Writes compare the configuration revision and fail on concurrent edits.

`add` requires either `--trust-candidate-code` or `--candidate-commit FULL_SHA`.
The latter requires the invocation's candidate to equal that commit; it never
loads older hook code against a newer candidate. Trust is stored separately for
each granted capability. Changing existing trust through `add` requires
`--replace`. `set` replaces the complete entry, for example:

```json
{
  "perms": {
    "jobdb": {"secrets": {"resolve": ["TEST_TOKEN"]}},
    "limit": {"jobdb": {"secrets": {"resolve": ["TEST_TOKEN"]}}}
  },
  "source_trust": {
    "jobdb.secrets.resolve": {"TEST_TOKEN": {"mode": "candidate"}}
  }
}
```

Storage uses `_c2j_hook_policy_tenant` and one
`_c2j_hook_policy_cell_<sha256(canonical repository identity)>` value per configured
cell. Documents contain `version: 1`, a `hooks` map, and, for cells, a `repository`
binding. Malformed relevant documents fail closed. The trusted controller records
both documents and their revision during job preparation. Changes apply to new
jobs; immediate credential revocation belongs at the secret provider.

Delegated tokens cannot read or write configuration. Administration commands
reject an execution context before discovering provisioning credentials. No
permission command resolves secret values.

## Execution boundaries

Configured authority belongs to the hook, not its shared caller. Input secret
materialization and direct resolution both check that authority, including when
values have been cached. Cross-repository includes retain their explicit
permission boundaries: same-repository includes inherit authority, while foreign
includes need explicit delegation. External recipe and op references accept
branches and tags, including when secrets or other permissions are delegated.
They use ordinary include/op resolution; authors need not supply a full commit
SHA. Local selector ops use the hook source repository and commit; that commit
must currently be fetchable by the extension source loader. Ordinary ops and
relative recipe includes support unpushed candidates.

Hooks may submit recipe children through c2j's controller. The controller replaces
any claimed inherited authority with the actual ceiling and captured hook policy.
Raw delegated tokens have job submission disabled for this path. Arbitrary child
job types are rejected. Candidate-code grants may propagate within that ceiling;
commit-bound grants authorize the recorded invocation and are withheld from
arbitrary child bodies. A child's hook callback must independently satisfy its
recorded policy and inherited ceiling.

Hooks and their included recipes may declare `extensions.functions` using the
same CEL package loader as other recipes. Floating package references are resolved
when the hook definition is loaded and recorded for retries and replay. Imports
are available inside that hook invocation alongside inherited CEL functions.
Local package selectors use the hook source repository and recorded candidate;
like local selector ops, the package loader currently requires a fetchable commit.
CEL package execution keeps its existing behavior; any future changes should
apply to ordinary includes and hooks alike. Provider-side hook identity
authorization and cross-repository hook workspaces are outside this implementation.
