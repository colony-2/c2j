# Runner-resolved secrets through `context.secrets`

Starting a job requires permission to submit work, without reading its secrets.
Recipes reference JobDB secret names through `context.secrets`; executing
runners resolve values using their current lease. Submission, orchestration,
and story replay operate on unresolved references.

JobDB now supplies the provider and lease API described in
[JobDB secrets guide](https://github.com/colony-2/jobdb/blob/v0.0.30/docs/GUIDE-SQLITE-SECRETS.md). The c2j integration is now implemented; see [SECRETS.md](SECRETS.md) for the supported contract and current limits.

## Recipe contract

Recipe syntax:

```yaml
sequence:
  - id: publish
    op: command_execution
    inputs:
      env:
        GH_TOKEN: ${{ context.secrets.github_token }}
      run: gh release create ...
```

`context.secrets.github_token` produces an opaque reference, not a plaintext
string or a secret-store read. No authored `$secret` object or separate
top-level `secrets` namespace is needed. The namespace supports named access,
not enumeration, existence tests, or dynamic name expressions initially.

Only input/configuration fields explicitly marked as accepting secrets may
receive references. Retain `secret: true` for c2j input declarations and define
equivalent metadata for extension input schemas. Mark command/extension
environment values as permitted destinations; ordinary strings do not implicitly
accept secrets. Reject plaintext values/defaults for secret-marked fields.

Secret-marked inputs can forward an opaque reference through `inputs.<name>` to
another permitted field. Validate the destination at every include/child/op
boundary. Direct `context.secrets` references in included recipes remain subject
to provider authorization; namespace access itself grants no authority.

Preserve references as typed internal values with a reserved wire format.
Initially allow only whole-value expressions and forwarding. Reject secrets in
conditions, IDs, filenames, outputs, command interpolation, and transformations
such as concatenation, JSON encoding, or hashing. These restrictions keep
orchestration independent of plaintext.

## Execution lifecycle

1. **Start:** validate reference syntax and permitted destinations; persist
   unresolved expressions/references without checking secret availability.
2. **Allocate:** Pulse hands the runner its existing JobDB execution lease.
3. **Execute:** when an uncached invocation needs secrets, consult the running
   job's in-memory cache. Batch missing names with known upcoming references
   through `TaskContext.ResolveSecrets(ctx, names)`. Require all names needed by
   the current invocation, then inject only its values into a separate transient
   input copy or child-process environment. Durable task inputs remain unchanged.
4. **Finish:** persist permitted results and sanitized diagnostics; dispose of
   transient credentials. Missing or denied required secrets fail setup before the op.

Missing/denied names required by the current op produce a sanitized setup failure;
an empty returned value is valid and must not be mistaken for an absent name.
Preserve exact value bytes, including trailing newlines. Provider errors are
already sanitized by JobDB. A live miss resolves secrets inside the consuming task wrapper. It fills the local session and records only duration/error diagnostics, never plaintext. Preparation currently shares the op timeout; replay skips the provider.

## Job-local batching and memory cache

Keep resolved values in a cache owned by the current local job execution and
lease. All its tasks, including task retries, can reuse those values. Ten
successive ops using the same secret should require one provider lookup.
Concurrent tasks share in-flight lookups rather than fetching the same name
independently. This cache is runtime state, never part of serialized job data.

Collect named references across the already-resolved recipe/op tree, including
forwarded inputs and environment bindings. On the first live invocation needing
an uncached secret, fetch a deduplicated batch covering its needs and known
upcoming tasks, within JobDB's request/response limits. This can prefetch names
from branches not ultimately taken. Do not load extra op packages, execute
future templates, or resolve more includes solely to discover secrets. Newly
discovered references join a later batch; cache hits require no provider call.

Validate returned values before caching them, and check completeness for the
current invocation before starting it. An omitted future name must not fail the
current task and is not cached as a permanent miss; request it again if needed.
If a speculative batch fails, retry the current invocation's required names
alone before attributing failure to that invocation. Register every fetched
value for redaction, but expose only the current op's requested values to it.
A batch does not imply an atomic snapshot across different secrets unless the
provider guarantees it.

Routine renewal of the same lease preserves the cache. Discard it on job-attempt
completion, cancellation, lease loss/expiry, handoff, or runner shutdown. Child
jobs and new local executions start with independent caches. Never use cached
values to continue execution after lease loss. Honor credential expiry if the
provider supplies it; otherwise reuse values for this execution's lifetime.
Rotation/revocation of a backing secret is therefore not necessarily observed
until the next fetch or execution, even while the lease remains valid.

Submission, replay, and cached tasks trigger no fetches; an entirely replayed job
needs no secrets. Prefetch starts only when live execution needs a secret.
Reference names participate in task identity; secret bytes and their hashes do
not. Rotation does not invalidate recorded task outcomes. Retrieval follows
setup-time accounting and shares the op's timeout, while remaining
bounded by the provider and enclosing execution context.

## Pulse and provider responsibilities

Use JobDB's context method directly; no new c2j provider interface or
provider-specific Pulse secret injection is required. JobDB manages the local
encrypted SQLite provider or an injected external provider. Resolution uses the
renewed live lease; replay and expired invocation contexts reject reads.

The configured JobDB provider owns authorization. Its SQLite development
provider currently allows any valid lease in a tenant to read that tenant's
requested secrets; per-job restrictions require a policy-aware provider.
Child jobs resolve under their own leases. Pulse is trusted because it handles
the same bearer lease during handoff, even though it need not retrieve values.
Neither schema annotations nor recipe declarations grant additional access.

## Persistence and initial scope

Register resolved values for masking before invocation. Sanitize logs and errors
before logging or persistence, including extension stderr and validation errors.
Do not serialize transient inputs into chapters, client payloads, diagnostics,
outputs, or persistent caches. The job-local memory cache is the only reusable
plaintext cache introduced by this proposal. Reject detected secret values in
structured outputs rather than caching them as ordinary data. Secret files must stay outside workspace
snapshots and artifact directories. Arbitrary ops can transform or copy plaintext
into artifacts: masking cannot guarantee protection against untrusted code.
Per-op injection within a shared container is not isolation.

Start with named string references, schema-enforced destinations, forwarding,
JobDB batch resolution, and a job-local memory cache. Defer raw-secret upload,
generated-secret outputs, and secret-derived expressions. The implementation uses JobDB `v0.0.30`.

Acceptance checks should cover one lookup for ten successive ops sharing a
secret, batching different names across tasks, concurrent lookup coalescing,
incremental discovery, and omitted future names. Also cover forbidden positions,
missing versus empty values, cache reset between executions, lease renewal/loss,
zero fetches for replay or memory-cache hits, and absence of plaintext in stored
inputs/results, logs, errors, persistent caches, and diagnostic exports.

GitHub Actions provides useful precedent for a `secrets` namespace, scoped
access, explicit forwarding, and [input/environment injection](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets).
Its [OIDC model](https://docs.github.com/en/actions/concepts/security/openid-connect)
also illustrates issuing credentials to executing workloads. Our additional
requirement is preserving unresolved references throughout durable job history.
