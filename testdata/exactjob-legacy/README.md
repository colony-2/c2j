# Legacy committed receipt fixture

This test module pins JobDB `v0.0.94-0.20261119021921-f885723cc03c`
(`f885723cc03ce9c8b52365390dca9601109d2c7e`). The current module/SDK is unchanged.

Run from the repository root:

```sh
go test -mod=readonly -count=1 -v -timeout=5m ./pkg/jobrecovery -run '^TestExactJob'
```

The harness builds this module with `GOWORK=off`, starts its unmodified legacy
HTTP/SQLite backend in a separate process, and seeds selected-submit and sharing
restart receipts using its old SDK. c2j then replays the frozen requests using
its current SDK wrapper and owner recovery client. Complete handles and decoded
wire bodies match; repeated replay retains three jobs in each of two tenants.
One restart uses an explicit destination and one only its original request ID.
Changed input/boundaries conflict, revoked original authority is denied, and
counts remain unchanged. A fresh-create/independent-restart control stays on the
normal SDK path. Test middleware installs explicit fixture authority; it is not
a production authentication implementation.

The same old SDK emits additional wire vectors for fields that cannot all be
combined in one stored operation. These verify encoding parity, separately from
backend receipt success. Dependencies and prerequisites use separate cohorts;
the old server rejects supplying both in a single operation.

This is SQLite/HTTP compatibility evidence. It does not test PostgreSQL,
production authentication, deployed workers, or published-module acquisition.
