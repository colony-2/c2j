# Published JobDB server fixture

This module is test infrastructure for the retained-input bridge. It builds a
disposable real HTTP server from published JobDB commit
`67cce3c7a7954cc334b06f5d881a2b16d3f9c490` without upgrading the c2j client's
dependency, replacing modules, or copying a transport implementation.

The process creates a fresh temporary SQLite database and synthetic test-only
cell/project authority. Its private stdin/stdout pipe selects source faults and
bare/wrapped permission revocation. Those controls are not HTTP endpoints.
Closing stdin stops the server and removes the database. It must never serve
production jobs or be installed as a production executable.

From the repository root:

```sh
GOWORK=off go -C cmd/c2j/testdata/input-history-server mod tidy -diff
go test -mod=readonly -count=1 ./cmd/c2j -run '^TestInputHistoryCLIProcess(RetainedRecords|RemoteOrigin)$'
```

The tests build this module automatically and verify its linked JobDB version
and absence of dependency replacements. A prebuilt fixture can be supplied with
`C2J_TEST_INPUT_HISTORY_SERVER_BINARY`; it must pass the same identity checks.
The candidate verify hook requires the published-server metadata/pagination and
denial/producer/atomicity cases to execute successfully. The legacy server's
generic storage diagnostic remains an explicit negative compatibility control.
