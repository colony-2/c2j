# Migration: Single-Tenant PollWork

## Summary

`PollWork` now polls exactly one tenant per request. Downstream consumers must
replace the old optional `tenantIds` single-item array with a required
`tenantId` string.

Before:

```json
{
  "tenantIds": ["tenant-a"],
  "workerId": "worker-1",
  "capabilities": ["send_email"],
  "limit": 1
}
```

After:

```json
{
  "tenantId": "tenant-a",
  "workerId": "worker-1",
  "capabilities": ["send_email"],
  "limit": 1
}
```

This is a breaking API change. Requests that omit `tenantId`, send an empty
`tenantId`, or include legacy `tenantIds` are rejected.

## What Changed

The `POST /v1/jobs/poll` request body now requires:

```yaml
tenantId:
  type: string
  minLength: 1
```

The old field was removed:

```yaml
tenantIds:
  type: array
  minItems: 1
  maxItems: 1
  items:
    type: string
```

The Go runtime API changed from:

```go
swf.PollWorkRequest{
    TenantIds: []string{"tenant-a"},
}
```

to:

```go
swf.PollWorkRequest{
    TenantId: "tenant-a",
}
```

Worker engines with registered workers must now be configured with the tenant
they poll:

```go
engine, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    WithWorkerTenantId("tenant-a").
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
```

## Who Must Migrate

Migrate code that does any of the following:

1. Calls `POST /v1/jobs/poll` directly.
2. Uses generated clients built from `openapi/swf-runtime.yaml`.
3. Constructs `swf.PollWorkRequest` directly in Go.
4. Builds an engine with workers using `NewEngineBuilder().PlusWorkers(...)`.
5. Relied on omitting `tenantIds`, tenant-less polling, or remote runtime
   fanout across known tenants.

No storage or database migration is required. Existing jobs, chapters, leases,
metadata, and artifacts keep their existing tenant identity.

## HTTP Clients

Replace `tenantIds` with `tenantId`.

Before:

```bash
curl -X POST http://127.0.0.1:9047/v1/jobs/poll \
  -H 'content-type: application/json' \
  -d '{
    "tenantIds": ["tenant-a"],
    "workerId": "worker-1",
    "capabilities": ["send_email"],
    "limit": 1
  }'
```

After:

```bash
curl -X POST http://127.0.0.1:9047/v1/jobs/poll \
  -H 'content-type: application/json' \
  -d '{
    "tenantId": "tenant-a",
    "workerId": "worker-1",
    "capabilities": ["send_email"],
    "limit": 1
  }'
```

Do not send both fields during rollout. The server rejects any request
containing `tenantIds`, even if `tenantId` is also present.

## Go Runtime Clients

Update direct `WorkflowRuntime.PollWork` callers.

Before:

```go
leases, err := rt.PollWork(ctx, swf.PollWorkRequest{
    TenantIds:    []string{"tenant-a"},
    WorkerID:     "worker-1",
    Capabilities: []string{"send_email"},
    Limit:        1,
})
```

After:

```go
leases, err := rt.PollWork(ctx, swf.PollWorkRequest{
    TenantId:     "tenant-a",
    WorkerID:     "worker-1",
    Capabilities: []string{"send_email"},
    Limit:        1,
})
```

If the previous code omitted `TenantIds` to poll without a tenant filter,
choose the tenant set outside `PollWork` and split it into separate poll calls
or separate worker engines.

Before:

```go
leases, err := rt.PollWork(ctx, swf.PollWorkRequest{
    WorkerID:     "worker-1",
    Capabilities: caps,
    Limit:        1,
})
```

After:

```go
for _, tenantId := range []string{"tenant-a", "tenant-b"} {
    leases, err := rt.PollWork(ctx, swf.PollWorkRequest{
        TenantId:     tenantId,
        WorkerID:     "worker-1",
        Capabilities: caps,
        Limit:        1,
    })
    if err != nil {
        return err
    }
    if len(leases) > 0 {
        break
    }
}
```

Prefer one worker engine per tenant for long-running workers. A manual loop is
reasonable for custom schedulers, administrative tools, or short-lived probes.

## Worker Engines

If an engine registers workers, set the worker poll tenant before building it.

Before:

```go
engine, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
```

After:

```go
engine, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    WithWorkerTenantId("tenant-a").
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
```

The engine can still submit, inspect, cancel, replay, and read artifacts for
jobs in any tenant when those APIs receive explicit tenant-scoped job keys. The
`WithWorkerTenantId` value only controls what the worker loop polls.

To process multiple tenants, run one engine per tenant:

```go
tenantA, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    WithWorkerTenantId("tenant-a").
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
if err != nil {
    return err
}

tenantB, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    WithWorkerTenantId("tenant-b").
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
if err != nil {
    return err
}

go tenantA.Run(ctx)
go tenantB.Run(ctx)
```

## Generated Clients

Regenerate clients from the updated OpenAPI document before deploying the new
server.

Expected generated shape:

```ts
type PollWorkRequest = {
  tenantId: string;
  workerId: string;
  capabilities: string[];
  limit: number;
  longPollUntil?: string;
  leaseDuration?: string;
  metadataEquals?: MetadataPredicate[];
};
```

Remove downstream code that populates `tenantIds`. If your generated client
still accepts `tenantIds`, it was generated from an old schema and should be
regenerated.

## Remote Runtime Behavior

The remote Go runtime no longer performs tenant fanout for `PollWork`. It sends
exactly one `tenantId` to the remote server.

If you previously depended on remote runtime startup polls or no-tenant polls
to discover work across known tenants, replace that with explicit tenant
assignment at the worker process boundary. For example, pass the tenant through
configuration:

```bash
SWF_WORKER_TENANT_ID=tenant-a ./worker
```

```go
tenantId := os.Getenv("SWF_WORKER_TENANT_ID")
if tenantId == "" {
    log.Fatal("SWF_WORKER_TENANT_ID is required")
}

engine, err := swf.NewEngineBuilder().
    WithRuntime(rt).
    WithWorkerTenantId(tenantId).
    PlusWorkers(jobWorker, taskWorker).
    BuildEngine()
```

## Rollout

Treat the server and every polling client as one compatibility boundary. The
new server rejects old `tenantIds` requests. Older servers may ignore the new
`tenantId` field and interpret the request as having no tenant filter, depending
on their generated request decoder.

For remote deployments, use a coordinated rollout:

1. Regenerate and publish OpenAPI-derived clients.
2. Build downstream client and worker code that sends `tenantId`.
3. Deploy the new server and new polling clients together, or route old workers
   to old servers and new workers to new servers until the rollout is complete.
4. Deploy workers configured with `WithWorkerTenantId` or equivalent explicit
   tenant configuration.
5. Remove any compatibility code that builds `tenantIds`.

Do not run old and new polling clients against the same remote server pool
unless that pool is intentionally version-partitioned.

## Failure Modes

Missing tenant:

```text
tenantId is required
```

Legacy field:

```text
tenantIds is not supported; use tenantId
```

Go worker engine without a poll tenant:

```text
worker tenantId is required when workers are registered
```

Treat these as configuration or client-version errors. They are not runtime
lease conflicts and should not be retried indefinitely without changing the
request.

## Test Checklist

For each downstream consumer, verify:

1. No code references `tenantIds` for `PollWork`.
2. Poll requests include a non-empty `tenantId`.
3. Worker engine construction includes `WithWorkerTenantId` when workers are
   registered.
4. Multi-tenant worker deployments run separate poll loops per tenant.
5. Generated clients were refreshed from the current OpenAPI schema.
6. HTTP tests cover missing `tenantId`, empty `tenantId`, and legacy
   `tenantIds` rejection.
