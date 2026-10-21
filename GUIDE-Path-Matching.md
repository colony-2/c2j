# Matching document paths in recipes

`path_match(reference, candidates)` is a built-in CEL and template function. Use
it when an agent returns a filesystem-style path such as `outbox/design.md`,
but your published artifact is named `design.md`. No extension import is needed.
The function compares supplied strings in memory; it does not read files,
invoke a process, or fetch artifacts.

## Quick start

```yaml
outputs:
  match: '${{ path_match("outbox/design.md", ["design.md"]) }}'
```

The output is a native map, not JSON text:

```json
{"status":"matched","path":"design.md","candidates":["design.md"]}
```

Try the complete [example recipe](examples/path-match.yaml) from the repository
root:

```bash
c2j submit --recipe-file ./examples/path-match.yaml --inputs-json '{"reference":"outbox/design.md"}' --run --embed
```

Use `reply.md` as the reference to get an ambiguous result, `missing.md` for
absence, or `../design.md` for invalid syntax. The example needs no external
services or model calls.

## Arguments and results

```text
path_match(reference: string, candidates: list<string>) -> map
```

Every result has these three fields:

| Field | Meaning |
| --- | --- |
| `status` | `matched`, `not_found`, `ambiguous`, or `invalid`. |
| `path` | The exact original candidate string for `matched`; otherwise `""`. |
| `candidates` | All matches at the strongest matching level, sorted lexically and deduplicated. Empty for `not_found` and `invalid`. |

Missing, ambiguous, and syntactically invalid paths return data without failing
expression evaluation. Incorrect argument types, including non-string list
members, are expression errors.

## Matching rules

1. Validate the reference and the entire inventory. Any invalid candidate makes
   the result `invalid`, even if another candidate exactly matches the reference.
2. Prefer an exact original string match.
3. Otherwise normalize the reference and compare its full component suffix
   against candidates. Drop one leading component at a time until matches exist.
4. Return the unique match or all ambiguous matches at that first matching level.
   If no level matches, return `not_found`.

Matching is case-sensitive and uses complete path components. There is no edit
distance, substring matching, extension replacement, or preference for shorter
paths. Input order has no effect, and duplicate strings do not cause ambiguity.

| Reference | Inventory | Outcome |
| --- | --- | --- |
| `design.md` | `design.md`, `archive/design.md` | Exact match: `design.md`. |
| `/scratch/job/outbox/design.md` | `design.md` | Match: `design.md`. |
| `./outbox//design.md` | `design.md` | Match: `design.md`. |
| `feature.md` | `iter1/consultant/feature.md` | Match: `iter1/consultant/feature.md`. |
| `outbox/iter2/reply.md` | `iter1/reply.md`, `iter2/reply.md` | Match: `iter2/reply.md`. |
| `reply.md` | `iter2/reply.md`, `iter1/reply.md` | Ambiguous: both, sorted. |
| `sign.md` or `Design.md` | `design.md` | Not found. |

Exact preference applies before normalization. For example, `./design.md`
against `design.md` and `archive/design.md` is ambiguous: both match the
normalized suffix `design.md`, and neither equals the original reference.

## Accepted path syntax

References may be absolute and may contain repeated `/` separators or internal
`.` components. They must end with a filename. Empty references, trailing `/`
or `/.`, NULs, backslashes, and any `..` component are invalid. Names and spaces
are preserved without trimming.

Candidates must already be canonical relative file names. Empty names,
absolute prefixes, repeated separators, trailing separators, `.`, `..`, NULs,
and backslashes are invalid. An empty inventory returns `not_found` for a valid
reference.

Only `/` is a separator. The function does not expand `~` or environment
variables, interpret URI schemes, or resolve symlinks. These strings are handled
lexically under the same component rules. An absolute reference never grants
access to that filesystem location.

## Using a match to select an artifact

Build the inventory from the current turn's eligible published artifacts. CEL's
map comprehension can extract names from an artifact map:

```yaml
# Inside a sequence node after the node named "produce":
inputs:
  match: '${{ path_match(sequence.produce.outputs.document_path, sequence.produce.artifacts.map(name, name)) }}'
```

If only some artifacts are eligible, filter that map or supply a narrower list
before calling the function. Do not use every artifact ever produced by the job.

With the result available as `inputs.match`, a CEL condition can check:

```text
inputs.match.status == "matched"
```

In the matched branch, retrieve the original artifact reference from the same
map used to build the inventory:

```text
sequence.produce.artifacts[inputs.match.path]
```

Keep that reference's storage identity and content. Do not construct a new
artifact reference from the returned filename or reopen the agent's original
filesystem path.

Handle the remaining statuses in your recipe:

| Status | Suggested handling |
| --- | --- |
| `ambiguous` | Ask the originating agent to choose from the returned candidates. |
| `not_found` | Ask it to correct the reference or supply the missing document. |
| `invalid` | Check the reference and inventory syntax; the result does not distinguish which was invalid. |

Matching resolves names only. Keep checks for required documents, allowed
iteration prefixes, duplicate references, review requirements, and approvals.
Resolve linked references consistently, including `purpose_path` and related
`files` entries.

Agent repair is separate recipe logic. Preserve previous responses and
documents, provide diagnostics, bound repair attempts, and escalate exhaustion
to human review. Repair must preserve consultation limits and approval gates
and must not repeat job submission or artifact publication. The built-in does
not invoke repair or make workflow decisions.

## Go-template usage

For interpolated text, the same helper uses Go-template argument syntax:

```text
status={{ (path_match inputs.reference inputs.candidates).status }}
```

Use `${{ path_match(inputs.reference, inputs.candidates) }}` when a recipe field
needs the complete native map.
