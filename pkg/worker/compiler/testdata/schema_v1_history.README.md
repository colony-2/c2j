# Legacy schema replay fixture

`schema_v1_history.db.gz` was recorded with unmodified c2j commit
`f1a153b898c5` and its pinned JobDB dependency. Its key is in
`schema_v1_history.key.json`.

The root and a resolved include both declare `session: {type: object,
object_type: test.session/v1, required: true}`. The input is the synthetic
`objects.ValidationRef("test.session/v1")`; no real checkpoint files or
credentials are involved. A `scope_probe:run` task returns the session contract
name. The completed database contains the resolved inline recipe snapshot, include
contract, scope history, and task input/output.

`TestReplayPreSchemaVersionHistory` opens a disposable copy and replays it with
current code, no task workers, and an unavailable source resolver. Do not
regenerate this fixture with the new writer: it protects compatibility with
history written before recipe schema versioning existed.
