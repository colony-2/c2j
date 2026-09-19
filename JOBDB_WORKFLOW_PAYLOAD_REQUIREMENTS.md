# Requirements: durable workflow payloads and explicit suspension

Status: historical requirements proposal. JobDB commit
`85cc496c0f5280210b02821bfb3a6eaa07ed9a76` now provides separate client payload,
revision-checked patch/reset updates, preservation across task transitions, and
explicit workflow yield. Its implemented contract differs from this proposal:
ordinary chapter writes do not publish payload changes, and publication during
execution requires yield. See `MIGRATION-CLIENT-PAYLOAD.md` for the actual API.

## Scope

Applications need to publish evolving continuation information for external
schedulers while retaining the same job, its history, and ordinary workflow
replay. This information is opaque to JobDB. Resource matching, provisioning,
and application-specific requirement schemas are not required.

## Required behavior

1. A job worker can read the current lease's opaque payload and request a
   suspension carrying a replacement payload and ordinary routing/wait
   conditions. The request is authorized by that lease, not merely the job ID.
2. Successful suspension atomically publishes the payload and releases the
   current lease. Workflow execution stops without completing or failing the
   job. It cannot commit later progress under the revoked lease.
3. Time waits, dependency waits, task-worker handoffs, and both in-process and
   external task completion preserve application-owned payload fields. Changes
   to framework-owned fields must not discard unrelated application state.
4. The next execution and job listings observe the accepted payload. Storage,
   remote transport, and active/archive movement preserve its JSON meaning,
   including unknown namespaced fields and exact JSON numeric values.
5. Stale leases and cancelled jobs cannot publish new continuation state or
   revive execution. A lost suspension response leaves ownership uncertain:
   the old worker stops, and a replacement reads authoritative durable state.
6. Applications can make freshly resolved information visible to bulk/paginated
   readers without requiring a suspension solely for publication. Publication
   must be lease-fenced and monotonic. The API/storage representation is open;
   this is a read projection, not scheduler matching state.
7. Explicit suspension must be distinguishable from successful completion,
   failure, and not acquiring a lease. Consumers need enough information to
   return promptly after handoff instead of immediately reacquiring the same
   job in a tight loop.

## Relationship to NextNeed

`NextNeed` continues to identify work capability, including job versus task
work and human versus automated task handlers. Opaque continuation data is
independent: changing it need not change capability, and changing capability
must not erase it. Existing readiness, wait, cancellation, and capability
matching rules remain unchanged.

## Replay ownership

JobDB supplies durable chapters, payload transport, and lease fencing. The
application defines deterministic checkpoint identities and payload revisions
so replay does not apply an old continuation patch over newer state. Suspension
does not transfer process memory or guarantee exactly-once external effects.

## Conformance

The same tests must pass for direct, SQLite, toy, and remote runtimes:

- Round-trip application payload alongside framework routing fields through
  submission, leasing, each reschedule path, task completion, and listing.
- Preserve unrelated fields when framework task-wait information is added or
  removed, including externally completed human tasks.
- Verify atomic publication/release and rejection of subsequent old-lease
  writes, cancellation races, and lost-response retry behavior.
- Verify that a non-suspending projection publication is visible in bulk
  listings and cannot overwrite a newer lease's state.
- Verify unchanged `NextNeed` matching and ordinary workflow outcomes for jobs
  with no application-specific continuation state.

## Dependency status

The original inspection of v0.0.13 and v0.0.17 found no generic workflow-context
payload access/suspension API and payload replacement during task
handoff/completion. The client-payload release addresses those ownership and
suspension gaps. No resource-specific API was introduced. Not all requirements
in this earlier proposal, notably non-suspending publication, were adopted.
