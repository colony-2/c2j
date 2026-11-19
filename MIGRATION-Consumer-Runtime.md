# Retained consumer runtime

c2j now uses JobDB `v0.0.94-0.20261119021921-f885723cc03c` and accepts
`jobdb.ConsumerRuntime` for execution. Goal attachment is absent from c2j's
runtime adapters and test harnesses. Creation-time primary selection, shared
goal restart, goal failure, discovery and saved dependency waits remain supported.

Go integrations that initialize an execution-runtime embedding by field name
must change `WorkflowRuntime:` to `ConsumerRuntime:`. Callbacks and decorators
should accept `ConsumerRuntime`, or their existing smaller reader ports.
Inspection now accepts `jobinspect.Runtime`, which composes history, lease-event
and artifact reads; metrics history accepts `jobdb.JobRunReader`.

An interface embedding only forwards methods declared by that interface.
Preserve the optional ports your decorator supports explicitly: schema and
configuration administration, development-secret set/remove operations,
deletion, primary-selection capability reporting, delegation and HTTP lease
operations. Keep a named backing field when concrete storage access is needed;
anonymously embedding a backend promotes its retired
methods too. Do not replace unsupported errors or caller authority with defaults.

HTTP fixtures must use `remote.NewConsumerServer` or
`remote.NewConsumerServerWithOptions`, retaining their authorization and signing
options. The consumer registrar omits candidate PUT while preserving supported
routes. Rejecting attachment methods and middleware that hides that route do
not remove the capability.

Historical additional-membership fields and settled producer bindings still
decode. Collection preserves the saved producer even when its primary goal is
different. Historical fixtures stay read-only. Ordinary input/review file
attachments and result artifacts are independent of goal membership.
