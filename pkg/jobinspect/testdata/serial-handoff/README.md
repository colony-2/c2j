# Recorded serial workspace handoff

`prefix.json.gz` contains a projection of the JobDB execution history for Cortex
job `3McO3PDMSul8iEKaPoEg3vla7vH`, through the **after** boundary of the first
implementation cycle's `verify/story-review-tests`, attempt 1 (ordinal 42).
The data keeps the original order, native occurrence IDs, task types, relevant
provenance fields and artifact references. It omits unrelated recipe, task and
error payloads, logs, and artifacts. The test reconstructs compact chapters
from the retained records, grouped by their original ordinals.

`ordinal-28.diff.gz` decompresses to the exact saved `diff_from_parent.diff`
bytes from the first-cycle squash, attempt 1. The uncompressed length is
16,277 and SHA-256 is
`73f704efb7e7e84110c099efac8eabe9662ec825d2ea232d375ed18b8445d612`.
