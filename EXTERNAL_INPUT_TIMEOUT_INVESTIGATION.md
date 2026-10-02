# External input and recipe timeout investigation

## Current status

The original investigation used JobDB
`v0.0.19-0.20260919034646-71b6668a65db`. This checkout now uses v0.0.23.
The old findings must not be treated as current failures:

| Area | Current behavior | Coverage |
|---|---|---|
| External task deadline | JobDB schedules the earliest applicable deadline and records timeout without another external handoff. | `TestTimeoutReproJobDBExternalTaskDeadline` |
| Job deadline during external wait | The job becomes retrievable for timeout handling at its total deadline. | `TestTimeoutReproJobDeadlineEventuallyRecordsFailure` |
| Existing task history | JobDB appends a terminal timeout without overwriting completed chapters or rerunning completed tasks. | `TestTimeoutReproJobDeadlinePreservesExistingChapter` |
| Recipe scope recovery | c2j checkpoints scope entry and task admission; recovery retains the original budget. | `TestRecipeDeadlineAcrossRecovery`, `TestDurableTimeoutReplaysCompletedTaskAndRejectsNewWork` |
| Typed timeout persistence | c2j's schema accepts JobDB's structured timeout payload. | `TestC2JSchemaAcceptsTimeoutCompletion` |
| Dependency wait eligibility | `AwaitJobs` still publishes no deadline wake-up. An alternate route alone does not bypass dependency or future-wait gates. | `TestTimeoutReproAwaitJobsDoesNotScheduleDeadline`, `TestTimeoutReproCoreAlternateRouteDoesNotOverrideWaits` |

The tests use real runtimes and clocks: toy, SQLite, and SQLite exposed over HTTP.
The `AwaitJobs` publication check uses the HTTP runtime. PostgreSQL is not covered.

## Remaining requirement

A dependency-blocked job should be retrievable when its normal wait condition
is satisfied **or its applicable deadline expires**, preserving lease exclusivity
and terminal/cancelled state. JobDB's route selection alone does not provide this
eligibility guarantee. A worker can use the existing completion mechanism once
it can acquire the job; a new terminal-state API is not inherently required.

Possible upstream approaches include deadline-aware acquisition independent of
ordinary wait gates or a dedicated timeout-processing acquisition path with the
same fencing. Periodic workflow polling is a workaround with additional overhead.
This requirement is separate from the external-task bugs already fixed in v0.0.23.

## c2j behavior and deployment

See [recipe timeout recovery](TIMEOUT_RECOVERY.md) for scope anchors, completed
result replay, custom worker registration, and compatibility. The new checkpoints
change the chapter sequence. Existing timed histories and jobs pinned to the old
schema are not automatically migrated; deploy matching workers for fresh jobs.

## Verification

```bash
go test ./pkg/worker/compiler ./pkg/starter ./pkg/jobdbschema
go test -tags=timeoutrepro ./pkg/worker/compiler -run TestTimeoutRepro -count=1
```

Despite the historical `timeoutrepro` name, fixed upstream behaviors are now
regression assertions. Dependency-wait tests still characterize the remaining
limitation; their passing does not claim automatic deadline wake-up is supported.
The original upstream report is retained with a status note in
[JOBDB_EXTERNAL_TASK_TIMEOUT_BUG_REPORT.md](JOBDB_EXTERNAL_TASK_TIMEOUT_BUG_REPORT.md).
