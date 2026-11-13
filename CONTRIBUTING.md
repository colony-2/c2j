# Contributing

Use `internal/pathutil.CanonicalExisting` when recording the identity of an
existing filesystem object. Create the target first and check errors. This
helper returns an absolute, symlink-resolved name; it creates nothing. It is
neither a confinement check nor a guarantee against later filesystem changes.
Preserve aliases where they are part of the caller's contract. In particular,
runner fixtures keep aliased cache paths while storing canonical Nix identities
and asserting readiness before publishing a manifest.

For generated POSIX shell source, use `pkg/shellcmd.QuotePOSIX` for one literal
argument, `Literal` for exact text, and `LiteralLine` to append one newline.
These use fixed `printf` formats and separate data arguments. Arguments cannot
contain NUL. Keep redirection and control flow outside the helpers. Keep runtime
expressions such as `"$@"`, `"$spec"`, and logging with fixed-format `printf` as
shell expressions; quoting them as literals changes their meaning. Precompose
static nested executable scripts in Go with these helpers. These conventions
apply to generated scripts, not user-supplied scripts.

Ordinary tests in `internal/portability/conventions_test.go` inspect an explicit
inventory: toolenv wrappers and runner/fake-manager fixtures, recipetest's Nix
passthrough and repository-package fixtures, and compiler tool-setup fixtures.
They check shared identity adoption, representative local quoting/normalization
replacements, and direct `echo` commands including nested executable templates.
The lexer distinguishes comments and literal argument data from commands. It
checks the supported template forms, not arbitrary Go or shell programs.

When adding a generator to this inventory, list its exact function (including
receiver), expose its script construction to the checked assignments or output
sinks, and extend inspection for any new builder form. Add both rejection and
positive-control cases when changing the rules. Unknown builders and missing
inventoried functions fail the check. A deliberate portability test needing
`echo` must list its exact file/function/line in `allowedEchoTests` and put
`// portability:allow-echo <nonempty reason>` at that construction site. There
are currently no exemptions; whole files cannot opt out.

Run `go test ./internal/pathutil ./internal/portability ./pkg/shellcmd` and the
affected packages. Shell text contract tests require both `sh` and Bash.
Repository verification also runs `make check-mod`,
`go test -count=1 -v -tags=integration ./...`, and
`npx -y skills add . --list --full-depth`; see the existing build verify hook for
environment setup and isolation of live job variables. Preserve the optional
Docker/real-manager test conditions. Native Linux/macOS CI provides additional
platform confirmation.
