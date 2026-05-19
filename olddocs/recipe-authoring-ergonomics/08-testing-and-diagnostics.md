# Requirement 8: Testing And Diagnostics

## Does The Spirit Make Sense?

Yes. These features should not ship without recipe-test and story coverage, because their value is reviewability and debuggability.

## Proposal

- Make `c2j test compile` perform recipe semantic validation across all declared recipe paths in addition to producing canonical test IR. Today the compile path mainly compiles the test suite structure.
- Ensure `c2j test run` uses the same default helper registry and transition/payload/vars execution path as embedded recipe execution.
- Extend recipe-test result diagnostics with:
  - rendered vars by node path/scope;
  - transition evaluations and selected transition payloads;
  - helper/render errors with recipe paths.
- Add specialized assertion types for vars and transition payloads, for example `var_equals` and `transition_payload_equals`, rather than requiring tests to assert against raw diagnostics JSON paths.
- Extend job-story nodes to include rendered vars and selected transition payload, with a redaction/sensitivity policy.
- Redact rendered values by default when their key/path looks sensitive, and augment that with local Gitleaks-style value-pattern scanning for likely secrets.
- Improve error paths by carrying YAML path information through recipe parsing or by adding traversal context when resolving templates.
- For switch/table transitions, job stories and test diagnostics should show both the original switch/case structure and the expanded CEL expression used for deterministic execution.

## Risks

- Strengthening `c2j test compile` can break suites that currently compile structurally but fail only at run time.
- Capturing rendered vars/payloads in stories can leak sensitive user input. Key-name redaction alone will miss secrets stored under generic names.
- Accurate recipe-path errors are hard with the current struct decode path because YAML node path information is mostly discarded.
- Diagnostics schemas will need versioning or careful additive changes for clients.

## Clarifying Questions

Resolved.

## Decisions

- Use specialized recipe-test assertions for rendered vars and transition payloads.
- `c2j test compile` should validate all declared recipe paths where static/semantic validation is possible.
- Redact values whose key/path looks sensitive by default.
- Augment key/path redaction with local Gitleaks-style value scanning using regex, entropy, keyword prefilters, and allowlists. Treat this as best-effort redaction, not a security boundary.
- Job stories and test diagnostics show both original switch/case structure and expanded CEL transition expressions.
