# Jobs recorded before the numeric restart fix

`jobs.sql` is a frozen SQLite dump of two jobs executed with c2j production sources at `11574b555465d1558754b436f68ef5c7ac0912ff`, before the numeric restart fix. `jobs.json` identifies the jobs. The reader tests load a fresh database for each case; they never generate history using the current worker or rewrite the captured application data.

- `completed`: the old worker executed all three tasks and completed with count 2 and fraction 1.75. The patched worker must inspect/replay it and restart its final task, reusing the first two completed results.
- `in_progress`: the old worker durably completed `prior` and `inner`. The generator rescheduled the lease and cancelled the worker immediately after writing the inner task result, before the target ran. The patched worker must resume the same job, reuse both durable results and execute only target. A separate test restarts the inner scope from the unfinished source and checks that the source history and job metadata remain unchanged.

Both jobs use the optional CEL integer default and unchanged nested integer arithmetic, plus a fractional double. All artifacts were kept inline, so no blob files or secrets database are needed. The SQL includes the original SQLite `user_version` (11); Python's `iterdump()` alone omits it. No persisted record payload or numeric value was normalized. SQL SHA-256: `94b72ace08f0952eb0410f133bd0f4888732459689b82c27f7e34f5a76e5848a`.

The fixture generator is retained here, outside normal Go package discovery. To regenerate in scratch with Go 1.26 and the repository module dependencies:

```bash
mkdir -p /tmp/c2j-old-source /tmp/c2j-old-history
git archive 11574b555465d1558754b436f68ef5c7ac0912ff | tar -x -C /tmp/c2j-old-source
cp pkg/story/testdata/restart-pre-numeric/generate_test.go /tmp/c2j-old-source/pkg/story/legacy_generate_test.go
C2J_LEGACY_FIXTURE_DIR=/tmp/c2j-old-history go test -C /tmp/c2j-old-source ./pkg/story -run TestGeneratePreNumericRestartHistory -count=1 -v
python3 - <<'PY'
import pathlib, sqlite3
root = pathlib.Path('/tmp/c2j-old-history')
db = sqlite3.connect(root / 'jobs.db')
version = db.execute('PRAGMA user_version').fetchone()[0]
(root / 'jobs.sql').write_text('\n'.join(db.iterdump()) + f'\nPRAGMA user_version = {version};\n')
PY
```

Copy the generated jobs.sql and jobs.json together only when deliberately updating this compatibility baseline. Generated IDs and times vary; the checked-in fixture is fixed. The generator asserts three completed calls, two pre-interruption calls, successful old completion and an unfinished rescheduled job.

Run the current reader with:

```bash
go test ./pkg/story -run 'Test(Restart|Resume)PreFix' -count=1 -v
```

All three cases run over native SQLite and its loopback HTTP adapter. No live job, external server, Docker, old compiler download or source publication is required at test time.
