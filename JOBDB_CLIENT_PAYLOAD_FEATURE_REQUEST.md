# Feature request: Separate client-owned job payload from JobDB-managed state

## Summary

Provide a client-owned JSON payload associated with a job, with ownership and
lifecycle semantics independent of JobDB-managed state. JobDB must preserve
that payload unless an authorized client explicitly requests its replacement
or removal.

Clients must not need to understand, copy, or preserve JobDB's internal
serialization to maintain their own payload. Conversely, JobDB must not
interpret client payload content as framework configuration or bookkeeping.

This is a public contract requirement, not a prescribed storage layout or API
shape. Separate database columns, an internal envelope, typed fields, or other
representations are implementation choices.

## Motivation

Clients need to carry evolving application state across execution attempts,
waits, task handoffs, and resumptions of the same job. When client data and
framework bookkeeping share a publicly exposed payload contract, either side
can inadvertently overwrite the other's state. Clients also become coupled to
internal field names and serialization details.

The desired behavior is that ordinary workflow operations can change JobDB's
state without altering client data, and clients can update their data without
reconstructing JobDB's state.

## Requirements

### 1. Independent ownership and representation

- The client payload belongs to the job, not to a particular lease, worker,
  task result, or execution attempt.
- JobDB must not inject framework fields into the client payload, reserve
  application property names for internal use, or infer scheduling or workflow
  behavior from its contents.
- Framework operations must not merge, remove, or rewrite client-owned values
  except to fulfill an explicit authorized client update.
- JobDB-managed state may still include information clients need to inspect or
  change. Such information must be available through supported public
  contracts with defined meaning and authorization. Clients must not need to
  know whether it is stored in an internal payload, an independent field, or
  another representation.
- Separating ownership must not make previously supported operational
  information unavailable merely because its storage is internal. This does
  not imply that every internal detail must become public or mutable.

### 2. JSON value semantics, not byte preservation

- The client payload is client-defined JSON, opaque to JobDB with respect to
  application meaning. Clients receive their JSON value without a
  framework-owned envelope they must interpret or maintain.
- Byte-for-byte round trips are not required. Object member order, whitespace,
  and equivalent JSON serialization may change on return.
- Round trips must preserve JSON values and types, nested content, array
  order, and the distinction between a missing property and a property whose
  value is `null`. Numeric values must not be silently rounded or truncated.
- JobDB may validate JSON and enforce documented size or representation
  limits. Unsupported values must be rejected explicitly, not silently changed
  or partially discarded.
- The distinction between no stored client payload and a stored JSON `null`
  must be defined. Neither may ambiguously mean "preserve the previous value"
  in an update request.

### 3. Explicit updates and preserve-by-default behavior

Clients must be able to supply initial payload at submission and update it
during an authorized execution. Update behavior must distinguish:

- **Preserve:** leave the current stored value unchanged; the default when no
  client-payload update is requested.
- **Replace:** replace the complete value with client-supplied JSON.
- **Clear:** explicitly remove the stored value.

Preservation must not require a caller or workflow helper to read the current
payload and send it back. Omitting an update must preserve the authoritative
stored value, not overwrite it with a stale copy or an empty default.

No server-side application-aware merge or patch behavior is required. A client
can compute its own replacement value.

### 4. Preservation throughout the job lifecycle

Absent an explicit client update, client payload must survive:

- lease acquisition, renewal, release, expiration, and replacement;
- retries, time waits, dependency waits, and capability changes;
- handoff between job and task execution;
- in-process and external task completion;
- cancellation, terminal completion, and archival, subject to documented job
  retention and deletion policies.

Workflow helpers must honor the same contract as lower-level APIs. In
particular, recording a task result or clearing internal pending-task state
must not clear the client payload.

Operations that create a new job, such as restart or clone, must have a
documented payload-inheritance policy distinct from continuation of an
existing job. This request does not prescribe that policy's default.

### 5. Atomicity and authorization

- An authorized execution must be able to replace or clear client payload as
  part of rescheduling or suspending the same job, including when its work
  capability or `NextNeed` remains unchanged.
- When a payload change accompanies a handoff, the payload change, scheduling
  transition, and release of execution ownership must succeed together or not
  take effect. A replacement execution must not observe a partially applied
  handoff.
- Payload mutation must respect execution ownership and cancellation rules.
  An expired or superseded lease must not overwrite state from a newer
  execution, and an update must not revive a cancelled job.
- Clients must also be able to publish an authorized payload update without
  ending the current execution or forcing a scheduling transition solely for
  visibility. Such updates require the same ownership safeguards.
- The contract must define conflict and ambiguous-result behavior so clients
  can recover without blindly overwriting newer state. General unrestricted
  out-of-band writes are not required.

### 6. Read access and visibility

- Authorized clients must be able to read client payload through execution
  APIs, job retrieval, and job listings, including retained archived jobs.
- Public reads must distinguish client payload from exposed JobDB-managed
  properties, regardless of their internal storage representation.
- Accepted updates must be visible according to documented consistency
  guarantees. A new execution must receive the state accepted before its
  handoff; listing visibility must not require replaying workflow history.
- Reading or updating client payload must not require access to internal
  serialization or unrelated framework configuration.

### 7. Compatibility

- Existing clients that do not use client payload must retain their existing
  workflow behavior.
- Legacy operations that do not request a client-payload update must preserve
  it when operating on a job that has one.
- If an existing payload API combines framework and application content, its
  migration must be explicit. Existing fields must not silently change meaning
  or be reclassified based on ambiguous property names.
- Mixed-version behavior and any required rollout restrictions must be
  documented. Unsupported combinations must not silently discard client data.
- All supported backends, transports, and workflow entry points must provide
  the same observable ownership and preservation semantics.

## Acceptance scenarios

1. Submit a nested client payload, execute a job, wait, retry, hand off a task,
   complete that task externally, and resume. The JSON value remains unchanged
   unless a client explicitly updates it.
2. Replace the payload while rescheduling with unchanged `NextNeed`. The next
   execution receives the replacement, and the old lease cannot mutate it.
3. Change exposed JobDB-managed properties without supplying client payload.
   The stored client value is preserved without a client-side read/copy cycle.
4. Use client properties named like framework concepts, such as `run_policy`
   or `task_wait`. They remain ordinary client data and do not affect JobDB.
5. Verify preserve, replace, clear, and the documented JSON-null behavior are
   distinguishable across local and remote APIs.
6. Return JSON with reordered object members or different whitespace. Clients
   receive equivalent values, with array order, types, and numbers preserved.
7. Publish an update while continuing execution and observe it through job
   retrieval and listing under the documented consistency contract.
8. Race payload mutation with ownership loss or cancellation. No stale update
   overwrites newer state or revives the job; handoffs are not partially applied.
9. Complete and archive the job. Its final client payload remains available
   for the job's documented retention period.

## Non-goals

- Prescribing database columns, table layouts, internal payload formats, or
  specific method and field names.
- Exposing internal serialization as a client API.
- Adding application-specific scheduling, provisioning, or payload schemas.
- Replacing job metadata or durable task inputs and results.
- Requiring byte-identical JSON, generic payload search/indexing, or automatic
  application-level merge behavior.
