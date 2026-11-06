# Named cells

c2j uses JobDB's stored cell registry. A cell has a name, a generated stable ID,
and a canonical repository locator. Repository paths and metadata are no longer
cell identities.

## Create and list

```sh
c2j cell create root --repo github.com/acme/root --jobdb https://jobdb.example/tenant
c2j cell create payments --repo github.com/acme/payments
c2j cell list --all
c2j cell list --all --state ALL --json
c2j cell list --dependencies
```

`c2j cells` accepts the same listing flags. The default lists active registered
cells visible to the caller. `--state RETIRED` or `--state ALL` includes retired
records. `--dependencies` reads project routing configuration, without connecting
to JobDB. These routes do not register cells or grant permission to submit jobs.
`c2j cell add REPO` still edits that dependency configuration.

Creation registers the cell; it does not create a Git repository. Every cell must
be explicitly registered before submitting work, through an authorized admin
client or a live execution lease. A repository assertion cannot create a missing cell.
`root` is an ordinary name: it can be renamed or retired and grants no special
authority. Recipe operations require an explicit `cells.create` grant.

## Create cells from recipes

Grant cell creation on the operation that runs the CLI or extension:

```yaml
- id: provision
  perms:
    jobdb:
      cells:
        create: true
        cellNames: [reports]
  op: command_execution
  inputs:
    run: >-
      c2j cell create reports --repo github.com/acme/reports
      --description "Reporting service"
      --request-id "$C2J_CURRENT_JOB_ID:create-reports"
```

Creation is denied by default. `cellNames` optionally restricts creation to exact
names; omitting it permits any valid name within the enclosing authority.
An explicit list must be nonempty and cannot contain wildcards or expressions.
The same fields work in `perms.limit.jobdb`, and nested scopes cannot widen a
limit. An explicit grant replaces inherited permissions, so include any needed
Git secret or job-submission grants separately.

Inside a running operation, `c2j cell create` uses the authenticated task broker
and its live restricted lease. It always targets the owning job's tenant; an
explicit `--jobdb` is rejected and ambient JobDB configuration does not retarget
it. Broker failures or denial do not fall back to administrative access. Go ops
can call `permissions.FromContext(ctx).CreateCell(ctx, request)`.

Use the same `--request-id` and creation fields to reconcile retries after an
uncertain response; without it, creating an existing name is a conflict.
Creating a cell does not grant permission to submit work there. Submission still
requires `jobs.submit` and JobDB's existing target-cell authority. Creation also
does not provision the Git repository.

## Submission and configuration

```yaml
self:
  cell: payments
  repo: github.com/acme/payments
  ref: main
root:
  repo: github.com/acme/root
```

Set `self.cell` explicitly, including when `base: go` supplies repository routes.
Use `--cell NAME`, optionally with `--cell-repo REPO`, on submission. Names cannot
be repository URLs or local paths. Hosted HTTPS, SSH and canonical locators are
normalized by JobDB; local paths are not registry locators. Clone routing and Git
credentials remain separate settings.

An existing name resolves through the registry. A configured or explicit repo is
an assertion and must agree with the registry. After an administrative repository
edit, update a stale local assertion or resolve by name without it. Submission
never overwrites the registered mapping.

## Workspaces and child jobs

```yaml
workspace:
  cell: payments
  repo: github.com/acme/payments # optional assertion
  ref: main
```

`workspace.initial: empty` also requires a repo assertion and can prepare an
uninitialized repository without registering a cell. Child recipes and child-group
entries accept `cell_name` and optional `cell_repo` separately. A child starting
from the active workspace carries its repository assertion and snapshot.

A job's owning cell is independent of its active workspace. `LookupCell` can
resolve any active cell in the lease's tenant, even with submission disabled.
Git credentials authorize workspace access. JobDB owner leases allow tenant-wide
submission unless explicitly restricted by trusted server policy; delegated
leases and c2j recipe grants still require submission authority. Recipe grants
can narrow submission further:

```yaml
perms:
  jobdb:
    jobs:
      submit: true
      cellNames: [payments, reports]
```

An omitted `cellNames` retains the enclosing ceiling. A present list must be
nonempty. Nested grants cannot widen it. Broker resolution and submission use the
live owning lease; they never recover from denial using administrative credentials.

## Execution, replay and deployment

The job's stored repository snapshot governs execution and restarts. A registry
repo edit affects fresh jobs only. The compiler records the server-supplied cell
identity before executing the recipe, so renames cannot change template inputs
when resuming or replaying. Inspection shows JobDB's current canonical name and
stable ID. Recipe schema versions 1 and 2 remain supported on fresh jobs.

This upgrade uses JobDB main commit `24f616ca4ffe` and requires a coordinated
fresh-storage cutover. Old JobDB stores, jobs, supplied leases, captured execution
contexts and compiled
histories are incompatible; there is no old-server or metadata-identity adapter.
Keep source repositories and edited configuration, explicitly register cells, and
submit fresh work. Do not reuse old embedded databases. Upgrade workers and clients
together.
