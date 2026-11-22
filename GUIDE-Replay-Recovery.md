# Recover jobs after replay or infrastructure errors

`c2j run`, `c2j run one` and `c2j run with-lease` exit 1 when a worker cannot
safely replay or read durable execution state. The invocation stops without
initiating job failure completion or rescheduling. Renewal owned by that runner
stops before diagnostics are read. Prior committed chapters and external side
effects remain; an error after a mutation may mean that mutation committed.

The worker writes one JSON diagnostic to stderr, separately from stdout progress.
It contains `message`, `jobid`, `tenant_id`, `ordinal`, `writers`, `versions`,
`reason`, `operation` and a recovery hint. Backend error bodies and credentials
are excluded. Go callers retain the original typed error through `errors.Is` and
`errors.As` with `*jobdb.ExecutionInfrastructureError`.

`versions` identifies the current c2j build and linked JobDB library, including
c2j's revision when known. It does not identify the remote service version.
`writers` contains observed c2j/JobDB combinations from individual stored version
records. Recorded c2j revisions distinguish equal version labels. The current
worker appears there only if it was recorded in retained provenance.

`writers_scope` is `retained_execution_provenance`. `writers_status` is `complete`,
`partial` or `unavailable` within that scope, rather than a physical-writer audit.
The diagnostic retains scan ceilings, coverage issues, omitted counts and the
number of unexamined records. Missing legacy metadata, corrupt records and limits
remain visible. `ordinal` is a physical chapter ordinal, including zero; it is
null when the error supplies no physical position. For example, current JobDB
logical descriptor mismatches supply no physical ordinal. A prerequisite error
can name the prerequisite job and its chapter instead of the invoking job.

The optional JobDB reader limits each lookup to 64 pages, 10,000 records,
16 MiB scanned, 512 pairs, 128 issues and a 256 KiB response. Its elapsed budget is
1.5 seconds; c2j also supplies a two-second context deadline. No full-history
reader is used as a fallback. Unsupported services, denied or expired credentials,
transport failures and malformed responses produce unavailable provenance while
preserving the primary invocation error. A lookup does not renew the lease or
broaden job-read permissions.

Repeated eligible claims whose execution leases expire reach the existing crash
concern threshold. The default requires the initial lease and five expired
reclaims; elapsed time without further claims does not advance the counter.
Before clearing concern, replace incompatible workers or repair the storage or
service fault. Then use the existing operator command:

```bash
c2j job clear-crash-concern JOB --reason 'incompatible workers replaced'
```

A matching worker can resume the same job when its scheduling constraints allow.
Already completed jobs cannot be restored by clearing concern. Keep unpatched
workers out of dispatch: they can still permanently fail incompatible jobs.
Deploy the adopted JobDB service and pgjobdb changes for native PostgreSQL
classification and remote provenance support. This change performs no deployment,
automatic history repair, rollback or crash-policy change.

Malformed authored recipes, application output validation and ordinary operation
failures keep their recipe catch/retry behavior. Corrupt c2j submission and private
result protocols, required invocation attachments and unreadable checkpoints are
execution infrastructure failures instead.
