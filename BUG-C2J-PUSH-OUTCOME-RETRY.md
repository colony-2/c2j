# Publication can succeed before its task outcome is recorded

Status: deferred; independent of empty-workspace support.

`pkg/git/squashrebasemerge/operation.go` creates a squash commit and pushes it
inside the operation. `pkg/worker/ops/op_executor.go` persists the resulting Git
snapshot and returns the task outcome afterward.

If the push succeeds but its response is lost, or the worker stops before the
outcome is stored, a retry starts from the previous recorded snapshot. It may
create a different squash commit (for example, with a new committer timestamp)
even though the first commit is already remote. The retry can fail or repeat
publication instead of recognizing the prior success. Recorded-task replay is
not affected: the missing outcome and external side effect are the problem.

Expected: reconcile an ambiguous publication without overwriting unrelated
remote work or creating duplicate history. Possible approaches include retaining
the exact publication candidate before the push, or reconstructing it
reproducibly and checking the destination for that exact commit. This report does
not choose an approach or require a new recipe-level receipt.

Regression coverage should inject failure after the remote accepts a push but
before local task completion, then retry on another worker. Cover both existing
branches and first publication, plus a destination changed by another writer.

Empty-workspace delivery explicitly defers this lost-response guarantee. Its
first-publication push still requires the destination branch to be absent and
must never replace a different existing branch.
