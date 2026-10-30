# Replay copying measurements

Measured on 2026-10-29 in the current checkout, Linux/arm64, Go 1.27.2, with six benchmark CPUs. The initial measurements describe the investigation; the final section records the implemented change.

## Replay reproduction

Input: `job-3LIeAglwSRxG36xoys9kJOwiy19-chapters.json`, 162 chapters, approximately 7.3 MB. An in-memory WorkflowRuntime reader supplies the exported chapters/job metadata to the real `live.BuildJobRunStory`; it executes no tasks.

The checkout was c2j `fa36a8a` with JobDB `v0.0.64-0.20261029043940-9c44e3e3bfa1`; `run.log` came from older c2j `7af6c5c` and JobDB `v0.0.64`. These are local reproduction results, not timings from the original server.

| Measurement | Result |
| --- | ---: |
| Initial unchanged-history replay | 70.824 s |
| Instrumented unchanged-history replay | 71.892 s |
| Reconstruction error | nil |
| Final story status | failed |
| CloneAttempt calls | 1,670 |
| Resolver template-data JSON round trips | 21,784 |
| Total JSON bytes marshaled by those copies | 7,725,245,292 |
| Largest individual serialized template data | 4,011,969 bytes |
| Total allocated bytes during instrumented replay | 64,486,180,360 |
| Allocation events | 812,477,354 |
| GC cycles | 300 |

Total allocated bytes are cumulative allocation traffic, not peak resident/live memory. Instrumentation captured copy sizes and a deterministic reservoir of 32 resolver snapshots. It retained those samples, so the instrumented run is not perfectly equivalent to the baseline.

The initial profile contains 100.17 s sampled CPU across cores: `CloneAttempt` 67.70 s cumulative; background GC 26.27 s. The SIGQUIT stack in `run.log` is inside the same JSON copying path. The history's final recorded error is `fork/exec /usr/bin/bash: argument list too long`.

## Shape of the copied data

The 32 sampled copies contain 124,189 nested maps, 411,387 map entries, and 5,636 lists. These count repeated occurrences in copied contexts, not unique runtime objects.

- Median map size: 3 entries; 90th percentile: 5; **97.0% have at most 8 entries**.
- 211,708 string occurrences, but only 768 distinct string values.
- `states` contributes 11.70 MB, approximately 86.8% of the sampled serialized field bytes. Repeated artifact/object descriptors and accumulated state/run outputs dominate the structure.
- Long strings: 8,907 occurrences exceeding 200 UTF-8 bytes, representing 75 distinct string values and 2,856,797 payload bytes. That is 4.21% of string occurrences.

The important scale is the repeated copying of many small objects. These samples do not establish that all independently produced equivalent maps could be interned safely, and the proposal does not depend on interning them.

## Removing values over 200 bytes

To make the experiment precise, “value over 200 bytes” means a **scalar string leaf with more than 200 UTF-8 bytes**. Strings were replaced with empty strings, retaining map/list shape and keys. Removing an aggregate map merely because its serialized size exceeds 200 bytes would remove whole state trees and answer a different question.

In recorded task output data, excluding the root recipe source needed to execute replay, the transformation removes **103 of 6,072 strings**, totaling **40,915 of 185,515 string bytes**. It leaves 1,636 other scalar values alone. This count covers those task outputs, not every field/string in the chapter envelope or recipe source.

Replaying that altered export is **not a valid timing comparison**: changing result strings changes execution decisions. It stopped after 225 ms with a nondeterminism error at chapter 7 and missing recorded events. That is incomplete replay, not an optimization.

Instead, the copy microbenchmark takes the 32 actual template-data samples, changes only long string leaves, and runs the same JSON marshal/unmarshal into the real `templateData` type. Data preparation is outside the timed loop. Each operation processes all 32 samples; figures below are medians of three runs with a two-second benchmark duration.

| Copy method, same 32-context batch | Time | Allocated bytes | Allocation events |
| --- | ---: | ---: | ---: |
| Original JSON round trip | 109.32 ms | 118.31 MB | 1,312,942 |
| JSON round trip, strings >200 bytes emptied | 104.07 ms | 99.95 MB | 1,303,872 |
| Reflective deep copy, immutable strings shared | 47.34 ms | 63.38 MB | 1,493,282 |
| Shallow state/sequence/variable index copies, payloads shared | 5.85 microseconds | 24.35 KB | 120 |

Emptying long strings reduced copy time by **4.8% (1.05x)**, allocated bytes by about 15.5%, and allocation events by about 0.7%. An earlier three-run batch showed a 2.5% time reduction, so the effect is small compared with run-to-run variation and the larger structural cost.

The reflective copy is about 2.31x faster than JSON but still traverses the whole graph and creates even more objects. It is not a semantically validated replacement for JSON normalization. The shallow-index experiment only measures copying `Sequence`, `States`, `Vars`, and `Locals` maps plus the template-data header. It shares payloads/run references and omits all other resolver work, publication, and ownership enforcement. It demonstrates why immutable payloads are the first optimization to pursue, not a promised 18,000x application speedup.

## Flat storage experiment

An isolated diagnostic packed tree uses 24-byte pointer-free nodes with integer offsets into node/string buffers and a shared immutable key dictionary. Round-trip reconstruction passed equality checks on all 32 samples. Two benchmark repetitions gave:

| Operation, same batch | Approximate time | Bytes allocated | Allocations |
| --- | ---: | ---: | ---: |
| Copy node and string buffers | 1.225 ms | 17.83 MB | 64 |
| Copy nodes; share immutable strings | 0.749 ms | 10.15 MB | 32 |
| Build packed representation from native values | 25.59 ms | 91.60 MB | 1,594 |

The builder is an unoptimized prototype using growing buffers. Copy timings exclude construction, lookup/indexing, CEL/template access, writes, patching, and serialization. No packed-container implementation was added to the project.

An isolated benchmark also compared native `maps.Clone`, `tidwall/hashmap` v1.8.1 `Copy`, and copying an unindexed entry slice for `uint32 -> uint64` maps:

| Entries | Native clone | tidwall copy | Entry-slice copy |
| --- | ---: | ---: | ---: |
| 4 | 134 ns | 179 ns | 44 ns |
| 8 | 129 ns | 294 ns | 88 ns |
| 64 | 1,452 ns | 1,769 ns | 573 ns |

Entry slices omit lookup/index costs. Flat allocation by itself does not establish that a replacement hash map improves this workload. `tidwall/hashmap` copies its bucket array, but nested values remain shared unless separately copied; using pointer-free IDs can avoid pointer scanning in the table. [Library source](https://github.com/tidwall/hashmap/blob/master/map.go).

## Template access experiment

The resolved recipe contains 281 Go-template actions across repeated included recipes, including 16 `range`, 4 `if`, and 4 `index` actions. Its requirements extend beyond plain scalar substitution.

A separate test using Gonja v2.9.1 implemented `AttributeGetter`/`ItemGetter` on an object without backing map fields. It rendered dotted and bracket access plus a loop over two shallow lazy handles, producing `ok ok ab`. The access trace contained only the selected paths. This validates mediated keyed access and this loop technique, not full recipe compatibility. Gonja's general iteration/length paths still need native collection wrappers or further integration. [Gonja interfaces](https://pkg.go.dev/github.com/nikolalohinski/gonja/v2/exec#AttributeGetter).

## Reproduction artifacts

Temporary tools and raw results remain local; they may disappear when the environment is reset. The measurement experiments did not modify production Go source or dependencies; Go `-overlay` supplied temporary instrumentation/test files. The subsequent production implementation is described below.

- `/tmp/c2j-replay-debug/main.go`, `result.log`, `cpu.pprof`: initial replay harness, result, profile.
- `/tmp/c2j-replay-analysis/main.go`, `attempt.go`, `overlay.json`, `result.log`, `copy-stats.json`, `samples.json`: instrumented replay and samples.
- `sample_stats.py`, `sample-stats.json`: sampled data-shape analysis.
- `analyze.py`, `output-value-stats.json`, `trimmed-chapters.json`, `trimmed-result.log`: long-string ablation and invalid full-replay result.
- `bench_test.go`, `bench-overlay.json`, `final-copy-bench.log`, `metadata-bench.log`: copy benchmarks. Earlier `bench.log` also contains a subsequently removed all-strings-empty variant that failed timestamp decoding; use `final-copy-bench.log` for the successful comparison.
- `mapbench/packed_test.go`, `packed-bench.log`: packed round-trip test and map/buffer benchmarks.
- `mapbench/template_test.go`, `template-probe.log`: passing Gonja mediated-access probe.

Commands, run from `/src` unless specified:

```sh
go run /tmp/c2j-replay-debug/main.go job-3LIeAglwSRxG36xoys9kJOwiy19-chapters.json
go run -overlay=/tmp/c2j-replay-analysis/overlay.json /tmp/c2j-replay-analysis/main.go job-3LIeAglwSRxG36xoys9kJOwiy19-chapters.json
go test -overlay=/tmp/c2j-replay-analysis/bench-overlay.json ./pkg/template -run '^$' -bench BenchmarkReplayTemplateDataCopy -benchtime=2s -count=3
go test -overlay=/tmp/c2j-replay-analysis/bench-overlay.json ./pkg/template -run '^$' -bench BenchmarkReplayMetadataCopySharePayloads -benchtime=2s -count=3
```

From `/tmp/c2j-replay-analysis/mapbench`:

```sh
go test -run 'TestPackedRoundTrip|TestGonjaLazyAccess' -v
go test -run '^$' -bench 'BenchmarkPackedSnapshots|BenchmarkSmallMapCopy' -benchtime=1s -count=2
```

## Final implementation with serial JobDB scopes

The original shared-map probe reconstructed the job in 4.526 seconds, but exposed late writes from abandoned asynchronous scope callbacks. A context-bound writer rejected late writes but could not reject a result already published before its scope was canceled. Moving publication outside the owning scope fixed that outcome-selection issue. A second shared-map probe then completed in 4.369 seconds. These probes retained bookkeeping copies and were not the final implementation.

JobDB main commit `6d86c38fc234` makes scope callbacks and input builders synchronous, leaving task workers asynchronous. The dependency is now pinned to `v0.0.65-0.20261029235121-6d86c38fc234`.

Ordinary scopes now share data and execution bookkeeping through a shallow resolver header. Input builders use the current resolver directly. Inherited vars/locals/transition/failure payloads are shared; lexical shadowing creates a fresh binding map. `CloneForRestart` is called only when preparing a restart patch. Scope outputs publish after the owning scope accepts its result; repeated writes to the same completion position return an error. Context-writer locks/revocation have been removed. Existing native readers and latest-output/history shapes remain.

| Actual implementation, unchanged supplied history | Result |
| --- | ---: |
| Replay wall time | 1.879 s |
| Reconstruction error | nil |
| Final recorded story status | failed |
| Approximate speedup against initial 70.824 s baseline | 37.7x |

This is a local single-run comparison. The replay ran alongside part of the package suite. No overlay was used. The recorded terminal error remains `fork/exec /usr/bin/bash: argument list too long`; the replay itself succeeds.

Verification and artifacts:

- `/tmp/c2j-serial-full-tests.log`: template, compiler, story/live, and story suites passed (compiler 136.843 s).
- `/tmp/c2j-serial-race-tests.log`: targeted template/compiler race checks passed, including retry/restart, catches, workspaces, publication, and late task completion.
- `/tmp/c2j-serial-compile.log`: every repository package compiled with `go test ./... -run '^$'`.
- `/tmp/c2j-serial-snapshot-test.log`: regression detecting snapshots of prior outputs in ordinary execution/input builders and replay passed.
- `/tmp/c2j-serial-probe/main.go`, `replay.log`, `cpu.pprof`: actual implementation replay harness, result, and profile.
- `pkg/worker/compiler/scope_output_test.go`: accepted/rejected publication boundaries, nested state ownership, and no ordinary snapshots.
- `pkg/worker/compiler/execution_publication_test.go`: actual task finishing after timeout/catch cannot replace accepted outputs/history or Git state.
- `pkg/template/attempt_test.go`, `execution_output_test.go`: restart isolation and duplicate completion protection.

Reproduce the final replay from `/src`:

```sh
go run /tmp/c2j-serial-probe/main.go job-3LIeAglwSRxG36xoys9kJOwiy19-chapters.json
```
