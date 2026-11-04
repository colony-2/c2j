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
be explicitly registered with JobDB's `cells.create` administrative permission
before submitting work. A repository assertion cannot create a missing cell.
`root` is an ordinary name: it can be renamed or retired and grants no special
authority. Cell creation is never inherited from a job lease.

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

A job's owning cell is independent of its active workspace. Every lease defaults
to its own cell, including jobs owned by `root`. Trusted server policy can grant
additional existing target cells. Selecting another workspace never expands
that authority. Recipe grants can narrow submission further:

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

This upgrade uses JobDB main commit `e199a54304e7` and requires a coordinated
fresh-storage cutover. Old JobDB stores, jobs, supplied leases, captured execution
contexts and compiled
histories are incompatible; there is no old-server or metadata-identity adapter.
Keep source repositories and edited configuration, explicitly register cells, and
submit fresh work. Do not reuse old embedded databases. Upgrade workers and clients
together.
