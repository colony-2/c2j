# Git profiles and execution-scoped credentials

Target: c2j. Implemented API contract, 2026-10-23. Usage: [Git profiles](GIT_PROFILES.md).

Store validated Git profiles centrally and activate them automatically in fresh execution containers. A profile combines URL routing and credential references for multiple destinations. Credentials are opaque JobDB secrets; c2j does not mint tokens or implement provider-specific authorization.

## Commands

```text
c2j config get NAME [--format=json|raw]
c2j secret get NAME [--format=json|raw]
c2j git set NAME --file PATH
c2j git get NAME
c2j git list
c2j git remove NAME
c2j git activate NAME
c2j git clear
c2j secret git-credential get|store|erase
```

- `config get` and `secret get` use current execution authority, without ambient management-credential fallback. Config defaults to JSON; raw requires a string. Secret JSON preserves value/version/expiry; raw emits only the value.
- `git set/get/list/remove` manage persistent profiles using management authority, independently of execution containers. `set` validates before writing; `get` never returns secret values.
- `activate` replaces the current scope's Git profile; c2j normally invokes this internally. It cannot expand the scope's authorized secret access.
- `clear` idempotently removes profile routing and credential access, retaining deny-only helper configuration to prevent ambient fallback. It does not delete persistent records, revoke the lease, or affect other scopes.
- Standalone activation/clearing requires an execution-scope control channel. A subprocess cannot change its parent shell's environment; the runner owns environment propagation.

## Backing keys

All records are tenant-scoped. Config keys are literal names, not nested paths.

| Store | Name | Value |
|---|---|---|
| Config | `git-profile-ro` | Default read-only profile name, or null |
| Config | `git-profile-rw` | Default publishing profile name, or null |
| Config | `_c2j_git_profile_NAME` | Versioned profile record for `NAME` |
| Secret | `_c2j_git_secret_ID` | Credential created from `secret_file`; `ID` is a generated UUID in 32-character hexadecimal form |

For example, `c2j git set work --file profile.yaml` writes `_c2j_git_profile_work`. Profile names are case-sensitive and match `[A-Za-z][A-Za-z0-9_-]{0,127}`. Each supplied credential gets its own generated secret name, recorded in the entry's `secret_ref`; externally supplied references retain their existing names. No separate profile index is required: `git list` enumerates the profile-key prefix.

Reserve `_c2j_` for c2j-managed records, not every name beginning with `_`. Public defaults remain ordinary user-editable configuration. Generic c2j mutation commands, when provided, must reject `_c2j_` keys and direct users to the typed management command. Generic reads, including `config get _c2j_git_profile_work`, remain available under normal execution authorization; secret reads still obey the node's secret allowlist. Other underscore-prefixed names have ordinary user-key semantics.

The prefix denotes ownership, not confidentiality or a server-enforced write restriction. Direct JobDB writes can bypass c2j validation, so activation must validate stored records. Schema enforcement across all writers would require a separate JobDB contract. Profile removal must never delete externally owned secrets; managed-secret cleanup must account for remaining references and live executions.

## Profile schema

Illustrative input to `git set`:

```yaml
version: 1
entries:
  - url: https://git.example.com/team/
    rewrite_from:
      - https://upstream.example.com/team/
    username: automation
    secret_ref: team-read-token
```

Each entry's `url` is its destination prefix and credential scope. Rewrites preserve the suffix; direct destination requests use the same credentials. Routing and credentials are independently optional. Credential entries require a username and secret reference.

Provisioning may accept `secret_file` (`-` for stdin) instead of `secret_ref`: c2j stores the value separately and persists only a generated reference. Publish the profile after required secret writes succeed; clean up failed creation without deleting externally owned secrets.

Use a versioned schema, validated on both `set` and activation; direct config-store writes can bypass CLI validation. Reject malformed URLs, invalid protocol fields, ambiguous matches, rewrite chains, and cycles. Route by longest source prefix. Match credentials against the resulting HTTPS scheme, host, port, and path scope, preferring the most specific path with component boundaries. Profiles have no enabled/disabled state.

## Node selection and defaults

Add `git_profile` to common node metadata, including recipe roots, containers, states, includes, and operations. Accept a name, supported whole-value template, or explicit `null`; distinguish omission from null.

| Selection | Behavior |
|---|---|
| Explicit node value | Select that profile; null selects none |
| Explicit enclosing selection | Inherit unless overridden, including null |
| No explicit selection, root/ordinary node | Default to config key `git-profile-ro` |
| No explicit selection, designated push operation | Default to config key `git-profile-rw` for that operation |

The root's implicit RO default does not suppress a publishing operation's RW default. Explicit enclosing selections do override operation defaults. Descendants inherit the effective scope selection; on exit restore the enclosing selection, never a completed sibling's override.

Missing or null default configuration means no profile. Unknown profile names, invalid values, and failed config reads are errors. Default expressions are equivalent to `${{ context.config[?"git-profile-ro"].orValue(null) }}` and its RW counterpart. Recipes need not spell them out. Resolve configuration during node preparation.

The built-in `merge` and `squashrebasemerge` operations receive the RW default: `merge` publishes the prepared HEAD by fast-forward, while `squashrebasemerge` also prepares the commits before publishing. `squash`, `rebase`, `thinpackrebase`, local Git changes, and arbitrary command/agent nodes retain RO. A shell command containing `git push` never triggers RW activation. Missing RW configuration must not silently reuse RO or ambient credentials.

RO credentials must lack push permission at the server. Restrict the node's resolver, including generic `secret get`, so an RO subprocess cannot request RW secrets or activate an unauthorized profile. Profile selection alone is not a sandbox against code with broader execution authority.

## Automatic activation and clearing

1. Acquire/import the lease and start renewal. Resolve the RO default and establish the resolver before remote bootstrap. A declaration inside a recipe cannot govern fetching that recipe.
2. Resolve each node's selection before workspace preparation, extension fetching, or other Git operations. Activate it, or clear the previous selection when entering a scope with none.
3. Generate execution-owned rewrite and credential-helper config files, containing no secrets or lease tokens. Wire these into Git subprocesses with path-aware matching and a trusted absolute c2j helper path.
4. Isolate configuration and resolver access across concurrent scopes. On exit, close the departing scope and restore the enclosing environment. A scope selecting none retains deny-only helper settings; never leave stale RW access.
5. Carry selection/inheritance state into separate tasks and child jobs. Fresh containers regenerate configuration under their own authority.
6. On completion, cancellation, handoff, or lease loss, close resolver access, suppress pending responses, and clean up files.

Do not mutate shared process-global Git settings. Clearing does not restore ambient authentication: managed executions must not fall back to ambient credentials/helpers or interactive prompts. Cleanup cannot retract credentials already delivered or guarantee interruption of an active transfer.

## Resolver and helper contract

The helper implements [Git's credential protocol](https://git-scm.com/docs/git-credential). Validate requests against the active profile before resolving. Return configured username, resolved password, optional `password_expiry_utc`, and blank-line terminator. Reject newline/NUL injection. `store` is a no-op; `erase` may invalidate a scope-local cache but never renews or revokes the lease.

Use a protected execution-local resolver inside c2j with the latest lease capability. Helpers never import/renew leases or receive lease tokens. Bound requests and provider I/O without holding renewal locks. Initially resolve fresh on every acquisition, bypassing attempt-wide caching. Preserve expiry, reject unusable responses, and register secrets for redaction before returning them. Keep values out of errors, diagnostics, history, and artifacts; stdout is reserved for the requested output protocol.

Fresh resolution uses the current renewed `ExecutionLease.ResolveSecrets`, bypassing workflow-context caching. Git may reuse credentials within a command; refresh per HTTP request is not guaranteed. Never blindly replay a push after an uncertain remote result.

## Acceptance

- Provision elsewhere; bootstrap in a fresh container without ambient credentials or additional helper binaries.
- Multiple destinations route/authenticate correctly; mismatches fail before secret disclosure.
- Root/agent nodes use RO; `merge` and `squashrebasemerge` use RW; explicit overrides/null follow inheritance.
- An ordinary agent push fails with server-enforced RO credentials; its resolver cannot disclose RW secrets.
- RO → RW → RO, profile → none, errors, retries, and parallel nodes leave no stale configuration/access.
- Clone, fetch, ls-remote, publishing, recipe subprocesses, and permitted submodules use the selected profile; local Git requires no credentials.
- Acquisitions survive renewal; slow providers do not block heartbeats; lease loss suppresses pending replies.
