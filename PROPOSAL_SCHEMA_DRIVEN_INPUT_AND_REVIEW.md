# Input forms and document reviews

Status: the earlier extension-based review design is superseded by a single
`input` op. See the [recipe-author guide](GUIDE-Review-Recipe-Authors.md) and
[application guide](GUIDE-Structured-Input-And-Review.md).

## Review contract

A review is an ordinary input form with:

- `kind: review`, so consumers can reliably recognize it.
- `documents`, a named map of existing stored artifact references.
- Ordinary questions/fields, with optional file-upload questions.

A response contains ordinary answers and optional document attachments.
Applications use the shared input library to inspect the form and submit an
answer for its runtime-owned request ID. Documents retain their producer
references; returned files are persisted with the response and exposed as refs.

The input op already has internal form-generation and collection substeps.
It resolves and freezes the form, checks document availability, then waits.
No recipe-visible preparation op, Python extension, or helper include is needed.
No hashes, annotation policies, CriticMarkup contract, or special decision flags
are required. Immutable artifact references identify original documents; the
request ID identifies the occurrence being answered.

The runtime checks required answers, question IDs, basic answer types/choices,
request identity, and attachment availability before completion. Autofill uses
the same path and records an automation actor. Receipts retain request,
submission, actor, and acceptance time. Workspace execution context survives
completion. Ordinary forms remain compatible.

The application supplies presentation and authentication. Recipes own decision
routing, agent continuation, verification, and merge behavior. c2j neither
interprets returned documents nor applies their contents.

## Generic structured input

The separately useful `request` / `response_schema` mode remains available for
arbitrary structured interactions. Reviews do not use or require it. The former
review preparation extension and its generated review schemas have been removed.

## Verification and remaining limits

Tests cover ordinary-form compatibility, review discovery, autofill, invalid
answers and attachments, artifact persistence, SQLite reopen, stale submissions,
and a single-op review in a contextualized workspace with another job's document.

Applications still need to render the form and call the input library. There is
no separate review component to install. Historical receipt lookup and successful
resubmission after acceptance are not implemented. The separate
[JobDB completion investigation](JOBDB_EXTERNAL_TASK_COMPLETION_DISCUSSION.md)
remains deferred; this change selects no fix for it.
