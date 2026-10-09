# JobDB lease errors hide their cause and lose response metadata

## Resolution as of 2026-10-09

Fixed upstream in JobDB **v0.0.28** (`5c3fbaec0f4c6ecfb8f74aea585b278fa067896d`),
now pinned by c2j. The historical report below records the affected implementation.

JobDB now exposes `LeaseTransportError.Operation`, `Phase`, `Category`,
`Destination`, `ResponseReceived`, `StatusCode`, and `SafeMessage()`, while
preserving the original cause and lease-loss sentinel semantics. It retains HTTP
status on response read/decode failures and snapshot conversion causes. Its
`LeaseRenewalError` propagates safe diagnostic text through initial runner renewal
and heartbeat failures.

c2j consumes that API; the temporary Go error classifier has been removed.
Only c2j-specific invocation context, configured tenant destination, and conditional
loopback advice remain. Exchange timeouts do not trigger the Docker hint because
that phase also covers response-header waiting and does not prove a dial failure.

Verification includes subprocess tests through the real c2j entry point, flag
parsing, HTTP client, error wrapping, stderr output, and exit code for refusal,
TLS, malformed/truncated responses, HTTP 503/403, and credential-bearing URI
rejection. Runner tests assert diagnostic propagation and redaction for initial
renewal and heartbeat failures. JobDB's own diagnostic and supplied-lease tests
cover the lower-level matrix.

## Historical status and affected versions

Confirmed on 2026-10-08 against c2j's pinned JobDB dependency:
`github.com/colony-2/jobdb v0.0.26-0.20261004234514-ffe70ccf111c`.
Originally reported with c2j 0.0.63 in `ghcr.io/colony-2/shai-mega:latest`.
The original macOS Docker setup was not independently reproduced; the client
failures below were reproduced directly using the pinned dependency.

The original report requested the remote-client fix subsequently released in
v0.0.28. No JobDB source is vendored or patched in c2j.

## User impact

Running `c2j run with-lease` inside a container with JobDB configured as
`http://localhost:9047/c2` can fail before recipe execution with:

```text
import supplied lease: lease renewal transport failed (HTTP status 0)
```

The underlying error may be connection refusal, but neither it nor the
destination is shown. Zero is not an HTTP status. It also does not mean that no
HTTP response arrived: response-body reading and JSON parsing failures produce
the same message. Other consumers of remote lease renewal receive the same
defective diagnostic, including renewals after execution has started.

## Confirmed implementation issues

Paths below are relative to the JobDB repository at the pinned revision.

1. `pkg/jobdb/runtime/remote/supplied_lease.go`, `LeaseTransportError.Error()`
   (line 93), always formats `StatusCode` and ignores `Err`. `Unwrap()` correctly
   retains the error chain; changing the displayed message must preserve that.
2. `remoteExecutionLease.Renew()` (line 106) uses
   `KeepAliveLeaseWithResponse()`. On any returned error it constructs
   `LeaseTransportError{Err: err}`, leaving status at zero.
3. `pkg/jobdb/internal/runtimeapi/zz_generated.go`,
   `ParseKeepAliveLeaseHTTPResponse()` (line 6134), returns `nil, err` on
   `io.ReadAll` or JSON decoding failure. The received HTTP status and the phase
   of failure are therefore unavailable to the caller. A formatter change alone
   cannot recover this metadata.
4. `remoteExecutionLease.Renew()` (line 125) also discards the error returned by
   `executionLeaseFromAPI()`, keeping only the response status. Invalid execution
   state or payload revision can consequently be reported as a transport failure
   with **HTTP status 200**, without the conversion cause.
5. Suppressing error text currently protects credentials and response content.
   Appending raw `Err.Error()` would undo that protection: URL errors can carry
   credentials/query values, while custom transport and decoding errors can
   contain untrusted text. Safe diagnostics need an explicit contract.

## Minimal direct-client reproduction

Run this program from a Go module using the affected JobDB version. It needs no
real JobDB, Docker, recipe, or valid server-issued credential. A structurally
valid test capability is sufficient to reach the failing client paths.

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "net"
    "net/http"
    "net/http/httptest"

    "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
)

func main() {
    capability, err := remote.DecodeLeaseCapability([]byte(
        `{"version":1,"tenantId":"tenant","jobId":"job","leaseId":"lease","leaseToken":"test-only-secret"}`))
    if err != nil { panic(err) }
    probe := func(name, target string) {
        client, err := remote.New(target, &http.Client{
            Transport: &http.Transport{Proxy: nil},
        })
        if err != nil { panic(err) }
        _, err = client.ImportLease(context.Background(), capability)
        var transport *remote.LeaseTransportError
        if errors.As(err, &transport) {
            // Test-only diagnostics: do not print raw underlying errors in production.
            fmt.Printf("%s: %v; underlying type: %T\n", name, err, transport.Err)
        } else {
            fmt.Printf("%s: %v\n", name, err)
        }
    }
    listener, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil { panic(err) }
    target := "http://" + listener.Addr().String()
    listener.Close()
    probe("connection refused", target)
    for _, name := range []string{"invalid JSON", "truncated response", "HTTP 503"} {
        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.Header().Set("Content-Type", "application/json")
            if name == "truncated response" { w.Header().Set("Content-Length", "100") }
            if name == "HTTP 503" { w.WriteHeader(http.StatusServiceUnavailable) }
            fmt.Fprint(w, `{"broken":`)
        }))
        probe(name, server.URL)
        server.Close()
    }
}
```

Observed results:

| Failure | Formatted status | Underlying error |
| --- | --- | --- |
| Connection refused | 0 | `*url.Error`, wrapping dial/refusal |
| Invalid JSON in HTTP 200 | 0 | `*json.SyntaxError` |
| Truncated HTTP 200 body | 0 | `io.ErrUnexpectedEOF` |
| HTTP 503 | 503 | nil |

The closed-listener refusal reproduction has the usual small port-reuse race;
upstream unit tests should inject a deterministic dialer/transport instead.

## Requested public diagnostic contract

JobDB should own classification, credential-safe rendering, and the associated
low-level tests. Expose stable typed metadata usable through `errors.As`, rather
than requiring clients to parse strings or inspect internal generated types.
Exact names are up to the JobDB team, but clients need these concepts:

- **Operation:** lease import/renewal, or an explicit renewal operation that the
  caller can contextualize as initial validation.
- **Phase:** request construction, connection/TLS, response reading, JSON
  decoding, and lease snapshot conversion/validation. A phase must not be
  inferred from status zero or from an ambiguous EOF alone.
- **Failure category:** DNS, connection refusal/unreachable, timeout,
  cancellation, TLS, HTTP rejection, response read/decode, and unknown.
- **Response metadata:** whether an HTTP response was received and its actual
  status when known, including on body/JSON failures. Unknown is distinct from
  a numeric HTTP status, and an HTTP 200 decoding failure is not an HTTP rejection.
- **Destination:** a documented sanitized service destination (or a public safe
  destination formatter). Remove all URL user info and fragments; omit all query
  values or use a safe allowlist. Do not expose request headers, bearer tokens,
  response bodies, or arbitrary nested error text.
- **Safe human-readable detail:** callers can display this without knowing the
  transport internals. Preserve `Unwrap()` and existing `errors.Is`/`errors.As`
  behavior independently of the displayed text. Document that inspecting the raw
  cause is not a credential-safe logging API.

An additive extension of `LeaseTransportError` would minimize client migration.
Preserve the distinction between invalid/lost lease authority and an ambiguous
renewal failure. A read/decode failure may occur after the server extends a lease;
do not imply the renewal had no effect, retry automatically, or acquire replacement
work. Execution must continue to stop on failed renewal.

Use a hand-written client adapter if necessary to retain status before parsing;
avoid a fragile edit to generated client code that regeneration would overwrite.

## Regression tests owned by JobDB

Use injected transports/dialers and local HTTP/TLS servers, not external networks.

1. DNS failure, connection refusal, unreachable destination, dial timeout,
   header/body timeout, caller deadline, and cancellation. Preserve wrapped causes.
2. TLS certificate verification and handshake/protocol failures; safely handle
   unrecognized errors without exposing their raw strings.
3. HTTP rejection with real status, malformed JSON, wrong JSON field types,
   truncated/read-failing bodies, missing renewal snapshot, and invalid snapshot
   conversion. Verify phase, response-received metadata, and status retention.
4. Unknown custom transport errors and redirect failures containing credentials.
   Test URL user info, known and unknown query keys, URL-encoded secrets, lease
   headers, authorization headers, and server-reflected tokens in response errors.
5. Safe `Error()`/`GoString()` output across `%s`, `%v`, `%+v`, `%#v`, plus any
   documented structured diagnostic API. Never render status zero as HTTP.
6. Direct `ImportLease`, initial runner renewal, and heartbeat failures use the
   same diagnostic contract. Preserve `LeaseRenewalError`, lease-loss sentinel
   semantics, and the execution-stop guarantees already tested upstream.

## c2j responsibilities after the upstream fix

c2j adds the configured destination and explicitly says initial supplied-lease
validation failed before this invocation began job execution. It adds conditional
Docker advice for loopback refusal/unreachable failures without a received HTTP
response, including IPv4 and IPv6. It does not assert that the process is running
in Docker or advise changing the destination for response-decoding failures.

`cmd/c2j/internal/runjob/lease_diagnostic.go` consumes JobDB's safe message and
category metadata. It keeps the original error chain without interpolating raw
nested errors. Initial runner renewal and heartbeat diagnostics flow through
JobDB's `LeaseRenewalError` into c2j's exit error and story renderer; they do not
incorrectly claim that execution has not started.

c2j's URI parser also stopped echoing rejected raw URIs, which previously could
disclose user info or query credentials before importing any lease.

The c2j tests cover command output and exit status, CLI rendering, destination
redaction, loopback hint conditions, error-chain preservation, parser redaction,
and import failures that verify no recipe chapters are appended. Exhaustive
transport classification remains owned and tested by JobDB.

```bash
go test ./...
go test github.com/colony-2/jobdb/pkg/jobdb/runtime/remote \
  github.com/colony-2/jobdb/pkg/workflow \
  -run 'Test(LeaseDiagnostics|LeaseRenewalError|.*SuppliedLease|.*Renewal|.*Heartbeat)' -count=1
```
