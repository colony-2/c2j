# Recorded inbox inspection

`Service.GetJobRunStory` exposes an optional `inbox` on task-backed nodes,
including prior retries and past job attempts. It describes **recorded selections;
delivery is unverified**. Failed preparation may have delivered none of the files.
Available metadata does not guarantee retained source bytes.

`Inbox.Status` is `available`, `unavailable`, or `not_applicable`.
`available` with `entries: []` means a recognized invocation selected no bindings.
Absent inbox on an older response means unavailable. Pending steps without recorded
input are unavailable; containers and synthetic source-resolution tasks are not
applicable and never aggregate child entries. Existing single-task operations
collapse their task node into the operation node; that task-backed operation
carries its own inbox. Multi-task operation containers do not union their children.

Entries sort by `binding`, then `path`, and preserve aliases. `path` is a normalized
inbox-relative destination when determinable. A trailing slash appends the source
filename using worker destination rules. Recorded invocation `artifacts` bindings
are supported, including typed normalized values that the compiler merges into
that map before recording. Application `input` maps and arrays never establish a
selection, even if they resemble value descriptors. Input-only historical value
descriptors lack the type/selection evidence needed to reconstruct a binding;
they cannot be previewed through this API. No binding or filename is inferred
from them, and content lookup returns `binding_unknown`. Dependency `artifact_keys`
alone do not establish named inbox selections and remain available in their existing
fields. Unknown entry details preserve the binding with a reason.

Each entry has `source_kind` and `content`:

| content | Meaning |
| --- | --- |
| `stored` | `source_key` is the original source job/task/name key, including its recorded size. Use existing artifact APIs. Missing, expired or denied bytes remain reader errors. |
| `value` | The recorded descriptor supports on-demand value serialization. No generated artifact key exists. |
| `unsupported` | See `reason`; external content is never fetched. Expanded external sources and possible file URL directories omit unknown member paths. |

The new projection contains neither value payloads nor external URLs. It restores
private invocation metadata when necessary, but does not serialize values or open
stored source files. Existing input/output and artifact fields keep their behavior.

## Value content

Use `Service.GetInboxValue(ctx, GetInboxValueRequest{...})` with `ProjectID`, `JobID`,
explicit `JobAttempt`, the node's `InvocationID`, `TaskOrdinal`, `TaskAttempt`
(node `Attempt`), and an exact `Binding` from its inbox. For past job attempts, use
the containing recipe root's `JobAttempt`. Invocation IDs from latest nodes or
prior-attempt nodes are accepted with that explicit task attempt. Current compact
histories require the invocation ID because several tasks can share one ordinal.
Legacy task-run histories accept their exact unique ordinal/attempt tuple. No
selector silently chooses the latest attempt.

The result contains `Data`, `Filename`, and `Format`. Text strings remain literal;
JSON is compacted with a final newline, preserving null and numeric precision.
Only the exact retained resolved descriptor is passed to `artifacts.Ref.ValueBytes`.
The accessor accepts no arbitrary regeneration value, expression, path or URL,
never replays recipes, and never persists a generated artifact. Request `Binding`
identifies a selection, not a filesystem path to open.

The engine reads the requested tenant/job through the existing caller access
boundary and checks returned membership. Private attachments use the same retained
record reader and its permissions. Cortex owns its authorized transport and UI;
this package adds no HTTP endpoint.

`*InboxError` exposes a stable `Reason` without private error payloads:

| Reason | Meaning |
| --- | --- |
| `invalid_selector` | Missing explicit identity, conflicting job attempt, or ambiguous records. |
| `binding_unknown` | The exact recorded invocation has no such binding. |
| `history_missing`, `history_malformed`, `history_unsupported`, `history_denied`, `history_unreadable` | History cannot supply a supported invocation. |
| `attachment_missing`, `attachment_malformed`, `attachment_denied`, `attachment_unreadable` | Private retained metadata cannot be restored. Expiration is missing when reported as not-found; otherwise unreadable. |
| `stored_reference` | Use the entry's exact `source_key` with existing artifact reads. |
| `source_unsupported`, `format_unsupported`, `descriptor_malformed` | Content cannot be serialized from this descriptor. |

Inventory also uses `pending_input`, `non_activity`, `path_unavailable`,
`external_content_unsupported`, and `member_inventory_unavailable`.
Recognizable permission/not-found sentinels are classified; opaque backend errors
remain unreadable. Nothing falls back to the current execution context.
