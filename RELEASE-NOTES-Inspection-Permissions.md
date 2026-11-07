# Task inspection follows JobDB read permissions

Task inspection now permits backend-authorized reads of sibling, ancestor,
descendant, unrelated, and terminal jobs in the selected tenant. Reads remain
independent of submission grants and bound to the live task and execution lease.
JobDB permissions still apply.

`c2j job list` inside tasks now lists backend-visible tenant jobs. Scripts needing
only children should use `c2j job list --parent JOB`. The separate `list children`
command retains its existing selection behavior.

Upgrade workers to obtain this correction; updating only the CLI leaves older
workers' restrictions in place. Endpoints, request/response fields, and response
version are unchanged. Missing or rejecting task helpers never trigger ambient
credential fallback.

For Go callers, `jobinspect.Service.Parent` remains a default listing filter;
`Request.Parent` overrides it instead of producing a permission error. Configured
`Authorize` callbacks now run for every listing, receiving the selected parent
or an empty ID for unfiltered discovery. Callbacks must support that empty-ID
case. The task callback checks scope and lease lifetime rather than lineage.
