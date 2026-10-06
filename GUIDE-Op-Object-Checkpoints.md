# Immutable object checkpoints for ops

Ops can publish a typed object containing JSON metadata and named files or directories. Recipes route one reference; c2j stores the contents as a private JobDB artifact and restores an independent writable copy for each consumer.

This first version has explicit inputs and outputs. There are no `object_bindings`, automatic slots, implicit latest sessions, or mutable shared objects. Two ops can consume the same checkpoint concurrently or sequentially. Each can publish its own successor without changing the original.

## Recipe experience

For an extension that implements the session contract below:

```yaml
id: session-example
version: "1"
sequence:
  - id: start
    op: ./session-op
    inputs:
      prompt: Investigate the problem.
  - id: explore_one
    op: ./session-op
    inputs:
      prompt: Explore the first approach.
      session: "${{ sequence.start.outputs.session }}"
  - id: explore_two
    op: ./session-op
    inputs:
      prompt: Explore the second approach.
      session: "${{ sequence.start.outputs.session }}"
outputs:
  session: "${{ sequence.explore_two.outputs.session }}"
```

Both explorations start from `start`. To continue the first exploration instead, reference `sequence.explore_one.outputs.session`. Use a complete `${{ ... }}` expression to preserve the object value; string interpolation is not object routing. References can also travel through nested maps/lists, state outputs, includes, child inputs/results, and subsequent jobs in the same tenant.

A recipe accepting a submitted object declares its schema and binds the input into its root scope:

```yaml
input_schema:
  session:
    type: object
    object_type: c2ops.codex.session/v1
    required: true
inputs:
  session: "${{ inputs.session }}"
```

`object_type` is optional for recipes that forward objects without knowing their type. `type: object` here means an object checkpoint, not an arbitrary JSON map. In an extension's JSON Schema, ordinary `type: object` keeps its usual meaning; the annotation below identifies checkpoint inputs.

## Extension op API

Declare the same versioned contract on the input and output in `op.yaml`:

```yaml
input_schema:
  type: object
  required: [prompt]
  properties:
    prompt: {type: string}
    session:
      type: object
      x-c2j-object-type: c2ops.codex.session/v1
output_schema:
  type: object
  required: [session]
  properties:
    session:
      type: object
      x-c2j-object-type: c2ops.codex.session/v1
    assistantSummary: {type: string}
```

Contract names use `name/vN`, where the name contains letters, digits, dots, underscores or hyphens, and N is a positive integer. c2j validates the reference type before starting the process. The annotation works inside properties, array items, map values, composition keywords and local schema references.

The process receives hydrated input JSON on stdin:

```json
{
  "prompt": "Continue the investigation.",
  "session": {
    "ref": {"$c2j_object": "v1", "type": "c2ops.codex.session/v1", "...": "durable reference fields"},
    "metadata": {"session_id": "session-123", "producer_version": "example"},
    "files": {"home": "/invocation/objects/checkpoint-unique/files/home"}
  }
}
```

The abbreviated `ref` above represents a complete framework reference. Pass it through unchanged when returning an existing checkpoint. `metadata` is the op's JSON contract. Every named `files` path is local to this invocation, writable, and visible in the worker environment. c2j hydrates references recursively, including references nested in arrays and maps. Non-object inputs retain their existing format.

For a new checkpoint, prepare export files beneath the framework-provided `C2J_OBJECT_OUTBOX`. Emit an envelope on stdout:

```json
{
  "output": {
    "assistantSummary": "Investigation complete.",
    "session": {"$object": "next_session"}
  },
  "objects": {
    "next_session": {
      "type": "c2ops.codex.session/v1",
      "metadata": {"session_id": "session-123", "producer_version": "example"},
      "files": {"home": "/invocation/objects-out/session-home"}
    }
  }
}
```

Use the actual `C2J_OBJECT_OUTBOX` value, not the illustrative path above. The runner maps paths back to the host, validates them, freezes the contents, replaces each `$object` marker with its durable reference, and validates the final output schema. Markers can appear inside nested maps/lists. A draft can be referenced more than once; unreferenced drafts and unknown marker names are errors. Emit `output` as a JSON object when using this envelope. Existing extensions that do not use objects retain their current protocol.

To return the original checkpoint without publishing another one:

```python
print(json.dumps({"output": {"session": request["session"]["ref"]}}))
```

For a runnable example, see the [session extension fixture](pkg/ops/extensions/testdata/object-session/run.py) and its [manifest](pkg/ops/extensions/testdata/object-session/op.yaml).

The ordinary artifact outbox remains for user deliverables. Object contents do not appear in `sequence.node.artifacts`, child artifact maps, or native `GetInputArtifacts()`/`GetOutputArtifacts()` lists. Object storage is an opaque interface, not an access-control boundary; authorized debugging tools can inspect the backing JobDB artifact.

## Native Go op API

`OpDependencies` now exposes `Objects() *objects.Store`. Declare native output fields as `objects.Ref` (or a pointer for optional outputs). Add an `object_type` field tag so dry-run recipe validation can carry the contract to downstream consumers:

```go
type Output struct {
    Session objects.Ref `json:"session" object_type:"c2ops.codex.session/v1"`
}
```

The actual op reads and publishes through the store:

```go
snapshot, err := deps.Objects().Open(ctx, input.Session, "c2ops.codex.session/v1")
if err != nil {
    return Output{}, err
}
defer snapshot.Close()

var metadata struct {
    SessionID string `json:"session_id"`
}
if err := json.Unmarshal(snapshot.Metadata, &metadata); err != nil {
    return Output{}, err
}
// Resume the tool using metadata.SessionID and snapshot.Files["home"].
// Stop the tool and flush its durable state before publishing.
next, err := deps.Objects().Publish(ctx, "c2ops.codex.session/v1", metadata, snapshot.Files)
if err != nil {
    return Output{}, err
}
return Output{Session: next}, nil // Session has Go type objects.Ref.
```

`Open` always makes a fresh copy, including repeated calls within one op. Native paths are host paths. `Publish` freezes the supplied files immediately; later changes to them cannot change the archive. The archive is persisted with the task outcome. Task replay reuses the stored outcome and reference, without rerunning publication. Failed attempts can retain diagnostic task artifacts under existing JobDB rules, but a failed op does not deliver a successful successor to recipe routing.

Native test doubles implementing `OpDependencies` must add `Objects()`. Tests that publish need a store configured with a durable job/task identity and artifact read/write callbacks. The worker supplies these in production.

## Migrating c2ops Codex

The current c2ops implementation uses `sessionId`, inbox/outbox home state and a session-ID cache in `codex/pkg/codex/execute.go`. Its restore path can restore the ID-based cache after the inbox state. That behavior must not be used for object inputs: an explicit checkpoint must determine the resumed state, regardless of newer state associated with the same ID.

1. Add optional `session` input and `session` output to `codex/op.yaml` and the `codex/run_skill` manifest. Use `c2ops.codex.session/v1` for both if their state is interoperable. Keep prompt, status, summaries and outcome fields as ordinary values.
2. Decode the hydrated descriptor. Validate the op-owned metadata and required file parts. Resume using `metadata.session_id` and `files.home`; do not search a shared home or select a cache by session ID. If the object is missing or invalid, fail instead of silently starting a new session.
3. For a new session, create a private home. For a resumed session, use the restored private home or copy it into this invocation's staging directory. Set the spawned Codex process's `CODEX_HOME` to that directory. Keep repository changes in the existing c2j worktree; Git snapshots continue to carry those changes automatically.
4. After Codex exits, collect the complete resumable session state. Session ID plus a rollout file may be insufficient: preserve the supported version's required session files, SQLite state/indexes and other durable resume assets. Flush/checkpoint databases consistently. Do not copy only the main SQLite file while necessary changes remain in WAL files. Handle relocated absolute rollout paths within the Codex adapter; never recreate paths belonging to an earlier invocation.
5. Export only the resumable state into `C2J_OBJECT_OUTBOX/session-home`. Exclude credentials, authentication files, transient sockets, locks and unrelated host configuration. Inject credentials and invocation-specific configuration at execution time. The archive API accepts regular files and directories and rejects symlinks and special files.
6. Emit the object draft and marker. Keep the same session ID if that is what Codex reports; two different checkpoints can legitimately share a session ID. The checkpoint reference, not that ID, identifies the state.
7. Update recipes from separate ID/artifact bindings to `session: "${{ sequence.previous.outputs.session }}"`. Retain `sessionId` as an optional informational output during migration if useful. If both legacy resume inputs and a session object are supplied, reject the ambiguity. Keep legacy behavior available only through the legacy input path, with a documented deprecation period if needed.

This change supplies c2j's object API and migration guidance. It does not modify the separate c2ops repository or make its existing Codex selectors accept `session` automatically. Pin a migrated op version before switching production recipes.

## Storage and compatibility

Each checkpoint is one deterministic TAR artifact under `__c2j_objects__/`, containing a versioned manifest and named file parts. A reference includes its type, tenant, originating job/task artifact key, size and SHA-256 digest. On restore, c2j checks the type, tenant, digest and archive paths before exposing files. A digest detects corruption; it is not an authorization credential. JobDB continues to enforce access and retain the underlying records.

References remain valid while their originating JobDB task artifacts are retained. Forwarding a reference to another job does not duplicate its originating history or add a garbage-collection pin. Any future retention/GC feature must trace object references before deleting their source artifacts. Do not delete source jobs while dependent checkpoints are needed.

Metadata and the part manifest together are limited to 1 MiB. File data uses existing JobDB artifact storage limits. Metadata cannot contain references to other objects in v1; route multiple objects in ordinary outputs instead. There are no framework migrations between object type versions: a consumer must explicitly support or migrate an older contract and publish the result under the new type.

Existing Git workspace snapshots and their legacy artifact names are unchanged. This feature supplies explicit op-owned checkpoints; it does not replace Git's automatic per-workspace forwarding or introduce automatic session slots.

## Required op migration tests

Use a deterministic fake Codex process in ordinary CI, plus a version-pinned real Codex smoke test where available:

- Publish A, then resume A separately into B and C. Verify C sees A's complete files and metadata, not B's changes, even when all share the same session ID.
- Resume B and verify it sees B. Repeat the A branches concurrently and across separate workers/jobs.
- Stop/reopen the runtime between publication and consumption; remove invocation directories and any old session cache.
- Fail after changing a restored home, then retry from A; verify the retry sees A. Replay completed tasks and verify they are not invoked again.
- Include database-backed resume state and relocated rollout paths. Verify resumability, not merely that a session ID was returned.
- Exercise invocation-local object paths, child round trips, root output forwarding, nested object values, incorrect types, missing artifacts, corrupted archives and unsupported contract versions.
- Confirm ordinary recipe artifact lists contain user deliverables and do not expose checkpoint packs.
