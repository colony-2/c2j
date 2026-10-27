# Git profiles

Git profiles combine HTTP(S) credential scopes and optional URL rewrites. Profiles live in tenant configuration; credentials live in JobDB's secret store or provider. Provision them once, independently of execution containers:

```yaml
# read.yaml
version: 1
entries:
  - url: https://git.example.com/team/
    rewrite_from:
      - https://upstream.example.com/team/
    username: automation
    secret_ref: team-read-token
```

```sh
c2j git set read --file read.yaml --jobdb "$JOBDB_URL/team"
c2j git get read --jobdb "$JOBDB_URL/team"
c2j git list --jobdb "$JOBDB_URL/team"
```

Use `JOBDB_BEARER_TOKEN` when the management endpoint requires authentication. Management commands use the standard `--jobdb` → `C2J_JOBDB` → `.c2/config.yaml` `jobdb` setting, with the tenant in the URL path. They also support `--embed` (tenant `0`). For example, `jobdb: http://192.168.1.195:9047/c2` makes `c2j git list` use tenant `c2` without extra flags. `set` validates the entire document and uses the configuration revision to reject concurrent updates. `get` outputs the definition, never a credential value.

Destinations accept `https://` and `http://`, including HTTP test servers. Credential matching includes the scheme, host, port, and repository path; HTTP and HTTPS credentials are separate even on the same host and port. Rewrite sources accept HTTP(S) URLs or SCP-style SSH prefixes such as `git@github.com:`.

An entry may omit routing or credentials. Credential entries require both `username` and `secret_ref`. Multiple entries support different hosts or repository paths. The most specific destination path selects credentials; equally specific duplicates, malformed URLs, and rewrite chains/cycles are rejected. Credentials are matched after rewriting.

For a supplied credential, replace `secret_ref` with `secret_file`, a path relative to the profile file or `-` for stdin. Credential files must contain one nonempty value without newline/NUL. This uses JobDB's supplied-secret administration API, currently provided by the SQLite backend. Other providers should provision their secret externally and use `secret_ref`. Only generated references enter configuration. Removal/replacement retains old managed secrets to avoid disrupting live executions; externally referenced secrets are never deleted.

## Recipe defaults

Profiles named `read` and `write` are used automatically when `git-profile-ro`
and `git-profile-rw`, respectively, are absent. No mapping is needed for those
names. If the corresponding profile is also absent, no profile is selected.

To use other profile names, set ordinary tenant config keys:

```sh
printf '%s' read | jobdb config set git-profile-ro --value-file - --url "$JOBDB_URL" --tenant team
printf '%s' publish | jobdb config set git-profile-rw --value-file - --url "$JOBDB_URL" --tenant team
```

Create `publish` with a separately provisioned write credential. Root recipes and ordinary nodes default to `git-profile-ro`. Only built-in `merge` and `squashrebasemerge` default to `git-profile-rw`. An arbitrary shell/agent `git push` keeps its node's profile; the server must enforce read-only permissions on the RO credential.

Every executable node accepts `git_profile`. Explicit selections inherit through containers and override automatic defaults, including those of publishing operations:

```yaml
id: profiles
sequence:
  - id: read
    op: command_execution
    inputs: {run: git fetch}
  - id: publish
    op: merge
  - id: local
    git_profile: null
    sequence: []
```

Use a name to override, `null` to select none, or a whole-value template such as `${{ context.config[?"git-profile-ro"].orValue("read") }}`. Omission inherits the automatic selection. An explicit `null` in a node or tenant default disables profile selection, including the conventional fallback. Explicit names must exist; invalid names/configuration fail before Git work. The enclosing profile is restored after a node exits.

c2j activates profiles before workspace and extension preparation, and carries selection into child jobs. Remote recipe bootstrap uses the inherited selection or RO default. A recipe's own declaration applies after that recipe is fetched. Includes are expanded before execution: profiles needed to fetch them must be literals or config references, not expressions depending on runtime inputs/outputs.

## Execution commands

```text
c2j config get NAME [--format=json|raw]
c2j secret get NAME [--format=json|raw]
c2j git clear
c2j git activate NAME
```

These commands require a live execution socket. JSON is the default; raw configuration output requires a string. Secret reads are limited to the active profile and validated operation-input secret references. The credential helper resolves fresh values using the latest lease, preserves expiry, and never renews the lease itself.

Activation and clearing normally happen internally. Manual `clear` removes profile routing and credential access while retaining settings that prevent helper/prompt fallback. `activate` can restore only the profile authorized for that scope. Neither command mutates global Git configuration or persistent profiles. Parallel nodes use separate files and sockets; cancellation and completion close access. This is execution configuration, not an OS sandbox against code with broader authority.

Each task creates its own Git scope before preparation. The scope is passed directly
to task code, with no process-wide task lookup. Its credentials share that task's
redactor with ordinary secret inputs. A later task creates fresh files, a new
socket, and a new redactor, even when its job or task identifiers match another
execution. Clearing one task's profile does not change another task's selection.

## Backing names

| Store | Key |
|---|---|
| Config | `git-profile-ro`, `git-profile-rw` |
| Config | `_c2j_git_profile_NAME` |
| Secret | `_c2j_git_secret_ID` for supplied credentials |

`NAME` matches `[A-Za-z][A-Za-z0-9_-]{0,127}`; generated IDs are UUIDs without hyphens. Reserve `_c2j_` for typed c2j management APIs. Other underscore-prefixed keys remain user keys. Generic reads are allowed under normal execution authorization; use `c2j git set` to validate profile writes. Direct JobDB writes can bypass that validation, so c2j validates again on activation.

## Node permissions

See [Recipe node permissions](GUIDE-Node-Permissions.md) for `perms.jobdb`
grants, automatic Git RO/RW secrets, and cross-repository include ceilings.
Git profiles remain outside the JobDB permission namespace.
