# GoReleaser to GitHub Actions Release Plan

## Summary

Replace the previous GoReleaser Pro release job with a GitHub Actions release pipeline made from focused open source actions. The goal is still readability: actions own workflow integration, while tiny inline commands handle the parts where the platform tool is clearer than a low-adoption wrapper.

The revised approach uses actions for:

- Semver tag calculation and tag creation.
- Go setup, workflow artifact transfer, release creation, generated notes, release asset upload, and npm publishing.
- Cross-platform macOS binary signing/notarization.

For the actual Go binaries, use the Go toolchain directly from one Ubuntu runner. This repo builds with `CGO_ENABLED=0`, so Linux and Darwin `amd64`/`arm64` artifacts do not need separate runners. The Go command builds one target per invocation, but all four invocations can happen in one job without a GitHub Actions matrix. macOS binary signing/notarization can also stay on Ubuntu when we use the same cross-platform style GoReleaser uses.

Repo-owned code should be limited to npm package files that are part of the published package itself, not CI shell plumbing. The main exception is compact inline workflow commands for project-specific facts such as `go mod tidy` cleanliness checks, setting `GOPRIVATE`, invoking `go build` for the four release targets, and creating the `tar.gz` archives.

## Previous Release Behavior

The previous release path was:

- `.github/workflows/release.yaml` runs on pushes to `main`.
- The reusable `test` workflow runs first.
- `anothrNick/github-tag-action@1.75.0` creates the next `v*` tag, defaulting to a patch bump.
- `goreleaser/goreleaser-action@v6` runs GoReleaser Pro with `release --clean`.

`.goreleaser.yaml` did the following:

- Runs `go mod tidy` before release.
- Builds `./cmd/c2j` as `c2j`.
- Uses `CGO_ENABLED=0`.
- Builds `linux` and `darwin` for `amd64` and `arm64`.
- Injects release metadata:
  - `main.version={{ .Version }}` where `.Version` is the tag without the leading `v`.
  - `main.commit={{ .Commit }}`.
  - `main.date={{ .Date }}`.
- Creates `tar.gz` archives named like `c2j_0.1.2_Linux_x86_64.tar.gz`.
- Includes `README.md`, `LICENSE`, and `docs/*` in archives. There is no tracked top-level `docs` directory today.
- Writes `checksums.txt` with SHA-256 checksums.
- Creates or replaces the GitHub release for `colony-2/c2j`.
- Generates grouped release notes from commit messages.
- Optionally signs and notarizes macOS artifacts when Apple secrets are set.
- Publishes `@colony2/c2j` to npm using GoReleaser Pro's `npms` feature and `NPM_TOKEN`.

## Goals

- Remove `goreleaser-pro` and `GORELEASER_KEY`. The workflow dependency and `.goreleaser.yaml` have been removed; the repository secret can be deleted after the action-based release succeeds.
- Prefer maintained, narrow-purpose open source actions over custom release scripts.
- Keep each workflow responsibility visible as its own job or step.
- Preserve current artifact platforms, names, version metadata, release notes quality, and npm package behavior.
- Keep private module access through `READ_ALL_C2_REPOS`.
- Make supply-chain risk explicit by pinning action versions and preferring GitHub-owned, Apple-owned, StepSecurity-maintained, or widely adopted actions.
- Avoid GitHub Actions matrices for release builds unless a future native Apple packaging path, such as `.app`, `.dmg`, or `.pkg`, requires a macOS runner.

## Validation Status

Validated locally on 2026-07-02:

- `go mod tidy -diff` exits cleanly with no `go.mod` or `go.sum` changes.
- A single Linux runner can build all current release targets with `CGO_ENABLED=0`:
  - `linux/amd64`
  - `linux/arm64`
  - `darwin/amd64`
  - `darwin/arm64`
- The proposed release names can be produced directly:
  - `c2j_0.0.0-validation_Linux_x86_64.tar.gz`
  - `c2j_0.0.0-validation_Linux_arm64.tar.gz`
  - `c2j_0.0.0-validation_Darwin_x86_64.tar.gz`
  - `c2j_0.0.0-validation_Darwin_arm64.tar.gz`
- Archives containing `c2j`, `README.md`, and `LICENSE` extract cleanly.
- A consolidated `checksums.txt` can be generated with ordinary SHA-256 output in GoReleaser-compatible form.
- The runnable host artifact passed a smoke check: extracted `linux/arm64` archive prints `c2j version 0.0.0-validation`.
- `anchore/quill@v0.7.1` installs and runs on Linux, and can ad-hoc sign a generated Darwin Mach-O binary on Linux. This validates the cross-platform signing tool path, but not real Apple notarization.

Validated against primary action/tool metadata:

- The existing `anothrNick/github-tag-action@1.75.0` tag-creation behavior is intentionally preserved in the implemented workflow. Changing version calculation is a separate release-metadata/versioning decision.
- `softprops/action-gh-release@v3` supports the planned `tag_name`, `name`, `files`, `generate_release_notes`, `overwrite_files`, `fail_on_unmatched_files`, `previous_tag`, and `body_path` inputs.
- `actions/upload-artifact` and `actions/download-artifact` support the planned artifact handoff. Current major tags are newer than the old v4-era examples, so pin deliberately during implementation.
- npm trusted publishing supports GitHub Actions hosted runners, requires `permissions.id-token: write`, and requires Node `>=22.14.0` plus npm CLI `>=11.5.1`. `actions/setup-node` with Node `24` satisfies this.
- GitHub release assets now expose SHA-256 digests through the UI, REST API, GraphQL API, and `gh`, so GitHub-provided digests are a viable future replacement for `checksums.txt`.
- GoReleaser documents the cross-platform macOS notarization mode previously used here as `notarize.macos` backed by `anchore/quill`. Native `codesign`/`xcrun` is only needed for the native `macos_native` path.

Not fully validated without repository secrets or external service state:

- Real Developer ID signing and Apple notarization with `MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`, `APPLE_API_ISSUER`, `APPLE_API_KEY_ID`, and `APPLE_API_KEY`.
- npm trusted publisher configuration for `@colony2/c2j` in npmjs.com package settings.
- GitHub release publishing permissions and release overwrite behavior in `colony-2/c2j`.
- Exact release-note quality compared with GoReleaser's commit-regex changelog.
- Bit-for-bit archive parity with GoReleaser Pro output. The validated target is behavioral parity: names, root contents, version injection, checksums, and install behavior.

## Non-Goals

- Do not add Windows artifacts.
- Do not redesign application versioning beyond replacing the implementation.
- Do not move release publishing into a large all-in-one tool that recreates GoReleaser under a different name.

## Action Selection

Recommended default actions:

- Existing `anothrNick/github-tag-action@1.75.0`
  - Kept in place to avoid changing release triggers, version bump behavior, or the recent test gating around releases.
  - A future metadata/versioning pass can evaluate Python Semantic Release or another semantic-release implementation.
- `wangyoucao577/go-release-action@v1`
  - Candidate only if we decide a build action is more valuable than a single native Go build step.
  - Its docs make `goos` and `goarch` mandatory and describe multi-platform builds through a GitHub Actions matrix, so it should not be the default for this repo.
- `chihqiang/gobuild-action`
  - Candidate action for one-job multi-platform Go builds. It accepts a space-separated `archs` list, packages archives, generates checksum files, and outputs generated file paths.
  - Do not choose it without a supply-chain review; it is much lower adoption than `go-release-action`.
- `crazy-max/ghaction-xgo@v4`
  - Candidate if CGO cross-compilation becomes relevant. It supports a comma-separated `targets` list from one Linux runner.
  - Not the default because this repo currently uses `CGO_ENABLED=0`, and xgo adds Docker/toolchain complexity we do not need.
- `softprops/action-gh-release@v3`
  - Creates or updates releases, uploads assets from globs, overwrites files by default, supports `generate_release_notes`, `previous_tag`, `body_path`, and `fail_on_unmatched_files`.
- `actions/upload-artifact` and `actions/download-artifact`
  - Transfer built archives between the single build job, optional smoke/checksum jobs, and the release upload job.
- `anchore/quill`
  - Preferred match for the previous GoReleaser behavior. GoReleaser documents its cross-platform macOS notarization path as using `anchore/quill`, and this repo's previous Ubuntu GoReleaser job was compatible with that mode.
  - Signs and notarizes Mach-O binaries from any platform, including Ubuntu.
- `indygreg/apple-code-sign-action@v1`
  - Action-wrapped alternative around `rcodesign`.
  - Can run from Linux, Windows, and macOS runners, and can sign/notarize Mach-O binaries without a macOS keychain.
- `apple-actions/import-codesign-certs@v7`
  - Keep as a native macOS fallback only. It imports certificates into a macOS keychain, which is useful for `codesign`/`xcrun` workflows but not needed for the default Ubuntu binary-signing path.
- `actions/setup-node`
  - Use npm's own trusted publishing flow. Third-party npm publish actions are less useful here because npm's trusted publishing docs and npm-publish action docs both point tag-based releases at `setup-node` plus `npm publish`.
- `python-semantic-release`
  - Candidate for a future release metadata/version calculation pass.
  - It can determine versions and generate changelogs/releases from commit conventions, but adopting it would replace more than the GoReleaser publishing job.

Avoid by default:

- Generic archive actions with low adoption unless the native Go build/archive step cannot meet parity cleanly.
- A matrix build for `linux/darwin` targets while releases remain pure-Go `CGO_ENABLED=0`.
- An npm publish action that wraps `npm publish` without adding behavior we need.
- A custom shell script for every release stage.

## Target Repository Shape

Prefer checked-in configuration and package templates over generated shell output:

- `.github/workflows/release.yaml`
  - Preserves the previous `push.branches: [main]` trigger.
  - Preserves the owner-gated reusable test job.
  - Preserves the existing create-tag job.
  - Replaces only the old GoReleaser job with explicit build, GitHub release, and npm publishing jobs.
- `.github/release.yml`
  - Configures GitHub-generated release note categories and exclusions.
- `npm/c2j/package.json`
  - Checked-in npm package template for `@colony2/c2j`.
- `npm/c2j/bin/c2j`
  - Checked-in JavaScript shim.
- `npm/c2j/scripts/postinstall.js`
  - Checked-in installer that downloads the correct GitHub release archive and verifies it.

Do not add `scripts/release/*.sh` unless an action cannot express a required behavior.

## Proposed Workflow

### 1. Tag Creation

Keep the existing tag creation behavior.

Workflow behavior:

- Trigger on pushes to `main`.
- Keep the current owner guard: only run in `colony-2/c2j`.
- Run `.github/workflows/test.yaml`.
- Keep `anothrNick/github-tag-action@1.75.0` with the existing `DEFAULT_BUMP=patch`, `WITH_V=true`, `PRERELEASE=false`, and `RELEASE_BRANCHES=main` settings.
- Feed the created tag into the replacement build/release/npm jobs.

Do not change tag/version semantics as part of the GoReleaser removal.

### 2. Release Metadata

Use `softprops/action-gh-release` to create or update the release.

Recommended settings:

- `tag_name: ${{ github.ref_name }}`
- `name: c2j ${{ github.ref_name }}`
- `generate_release_notes: true`
- `fail_on_unmatched_files: true`
- `overwrite_files: true`

Use `.github/release.yml` for categories and exclusions. This means release notes become GitHub-native PR-label release notes rather than GoReleaser's commit-regex grouping.

If better release metadata is required, evaluate Python Semantic Release as a separate change. It is designed to determine SemVer versions and generate release notes/changelogs from commit conventions, but adopting it would also affect tag creation.

### 3. Go Build, Archive, and Asset Naming

Default to a single `ubuntu-latest` build job with `actions/setup-go` and native Go cross-compilation.

Why this is cleaner:

- Go cross-compilation is built into the Go toolchain for pure-Go builds.
- The previous GoReleaser config set `CGO_ENABLED=0`.
- The repo only needs four targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.
- A matrix would spin up four runners and scatter release output across four logs for work one runner can perform deterministically.

Recommended build job shape:

- `actions/checkout`
- `actions/setup-go` with `go-version: "1.26.1"`
- Configure `GOPRIVATE=github.com/colony-2/*` and private module auth.
- Run `go mod tidy` and fail if `go.mod` or `go.sum` changes.
- Set:
  - `VERSION=${GITHUB_REF_NAME#v}`
  - `COMMIT=$(git rev-list -n 1 "$GITHUB_REF_NAME")`
  - `DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)`
- Build the four targets with:
  - `CGO_ENABLED=0`
  - `GOOS`/`GOARCH` set per target
  - `go build -buildvcs=false -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" -o ... ./cmd/c2j`
- Sign/notarize Darwin binaries before packaging when Apple secrets are present.
- Package each target as:
  - `c2j_${VERSION}_Linux_x86_64.tar.gz`
  - `c2j_${VERSION}_Linux_arm64.tar.gz`
  - `c2j_${VERSION}_Darwin_x86_64.tar.gz`
  - `c2j_${VERSION}_Darwin_arm64.tar.gz`
- Include `c2j`, `README.md`, and `LICENSE`.
- Include top-level `docs/*` only if that directory exists.
- Prefer explicit archive paths, e.g. `tar -C "$staged" -czf "$archive" c2j README.md LICENSE`, so archive entries are root-level names rather than `./...`.
- Upload all four archives with `actions/upload-artifact`.

This is not a release script; it is a visible workflow step that lets Go do the build work. If we want even less inline shell, evaluate `chihqiang/gobuild-action` as an action-wrapped single-job builder, but its adoption level makes native Go plus GitHub-owned actions the safer first choice.

When to keep a matrix:

- If future `.app`, `.dmg`, or `.pkg` packaging requires Apple's native `codesign`, `xcrun`, `productsign`, or stapling tools, run that packaging path on `macos-latest`.
- If future release targets need CGO or platform-native tests, split only those targets into native jobs.

Do not use a four-way matrix merely to express four `GOOS`/`GOARCH` values.

### 4. Checksums

There are three choices.

Implemented parity choice:

- Use a short visible `sha256sum *.tar.gz | sort -k2 > checksums.txt` workflow step.
- Upload `checksums.txt` with `softprops/action-gh-release`.
- This avoids adding a low-adoption Docker action for one standard command.

Lower-maintenance future choice:

- Stop publishing `checksums.txt`.
- Rely on GitHub's built-in SHA-256 digests for uploaded release assets.
- Update the npm installer to verify against release asset digests from the GitHub Releases API.

GoReleaser-close but not exact choice if we use the `go-release-action` candidate:

- Let `go-release-action` publish per-asset `.sha256` files.
- This is simpler than a consolidated file but changes the release asset surface.

Keep `checksums.txt` for the first migration release, then decide whether GitHub release digests are enough.

### 5. Smoke Tests

Avoid a release smoke-test script. Add direct workflow steps:

- `actions/download-artifact` to collect archives.
- Extract each archive and run `./c2j version`.

This should run in the same single build job after packaging, or in one downstream `smoke` job after `actions/download-artifact`. A matrix is not needed here either. Current `c2j version` only prints the release version string, so the smoke assertion should check `c2j version ${VERSION}`. Commit/date are injected for `buildinfo.Info` parity but are not shown by the current version command.

### 6. npm Publishing

Do not generate the npm package through a release shell script. Check in the npm package files under `npm/c2j`.

Release job behavior:

- Use `actions/setup-node` with:
  - `node-version: "24"`
  - `registry-url: "https://registry.npmjs.org"`
  - `package-manager-cache: false`
- Ensure the npm CLI version is `>=11.5.1`; Node `24` currently satisfies this.
- Update the package version with npm's own CLI:
  - `npm version --no-git-tag-version "${VERSION}"`
- Run `npm pack --dry-run`.
- Publish with trusted publishing:
  - `permissions.id-token: write`
  - `npm publish --access public`

Package behavior:

- `postinstall.js` maps Node platform/arch to the release asset:
  - `linux/x64` -> `c2j_${VERSION}_Linux_x86_64.tar.gz`
  - `linux/arm64` -> `c2j_${VERSION}_Linux_arm64.tar.gz`
  - `darwin/x64` -> `c2j_${VERSION}_Darwin_x86_64.tar.gz`
  - `darwin/arm64` -> `c2j_${VERSION}_Darwin_arm64.tar.gz`
- It downloads from GitHub Releases.
- It verifies either:
  - `checksums.txt` during parity mode, or
  - GitHub's release asset digest after we intentionally drop `checksums.txt`.
- It installs the binary into the package `bin` directory.

This moves the complicated npm behavior into normal package source files where it can be reviewed and tested, instead of hiding it inside a CI generation script or GoReleaser Pro.

### 7. macOS Binary Signing and Notarization

Keep this on Ubuntu for the current CLI artifacts.

Why:

- The previous workflow already ran GoReleaser on `ubuntu-latest`.
- The previous `.goreleaser.yaml` used `notarize.macos`, not the native `notarize.macos_native` path.
- GoReleaser documents `notarize.macos` as the cross-platform mode backed by `anchore/quill`.
- This repo ships CLI binaries in `tar.gz` archives, not `.app`, `.dmg`, or `.pkg` artifacts.

Recommended path:

- Build Darwin binaries on Ubuntu.
- Before archive creation, run either:
  - `anchore/quill sign-and-notarize` against each Darwin binary, or
  - `indygreg/apple-code-sign-action@v1` against each Darwin binary.
- Use the existing secrets:
  - `MACOS_SIGN_P12`
  - `MACOS_SIGN_PASSWORD`
  - `APPLE_API_ISSUER`
  - `APPLE_API_KEY_ID`
  - `APPLE_API_KEY`
- Package the signed/notarized Darwin binaries into the same `tar.gz` names as today.

Native macOS runner fallback:

- Only use `apple-actions/import-codesign-certs`, `codesign`, `xcrun notarytool`, `productsign`, or stapling if the release format changes to `.app`, `.dmg`, or `.pkg`.
- Do not introduce a macOS matrix for the current CLI binary release.

## Migration Sequence

### Phase 1: Replace GoReleaser Job

- Preserve the existing release workflow trigger, test job, and create-tag job.
- Replace the GoReleaser job with explicit build, checksum, smoke-test, GitHub release, and npm publishing jobs.
- Keep all package-specific input values in top-level workflow `env`.

### Phase 2: GitHub Release Assets

- Use `softprops/action-gh-release` to own release metadata and final asset upload.

### Phase 3: npm Package

- Add checked-in `npm/c2j` package files.
- Configure npm trusted publishing for `@colony2/c2j`.
- Run `npm pack --dry-run` and an install test from the packed tarball.
- Publish one pre-release/canary if acceptable.
- Remove `NPM_TOKEN` after trusted publishing is confirmed.

### Phase 4: macOS Signing Parity

- Validate the Ubuntu-based `quill` or `rcodesign` signing flow on Darwin artifacts.
- Decide whether notarization remains required or optional for CLI tarballs.
- Do not delete GoReleaser until this is settled if notarization is release-critical.

### Phase 5: Remove GoReleaser

- Remove the GoReleaser job from `.github/workflows/release.yaml`. Done.
- Remove `.goreleaser.yaml`. Done.
- Remove `GORELEASER_KEY` from repository secrets.
- Keep rollback notes for one release cycle.

## Rollback Plan

- Roll back from git history if the first action-based release exposes a blocker that cannot be fixed quickly.
- If action-based GitHub asset upload fails before npm publish, rerun the tag workflow after fixing the action inputs.
- If npm publish fails, fix and rerun only the npm job for the same tag as long as npm has not accepted that version.
- If npm accepts a bad version, publish a new patch tag; npm versions are immutable.

## Open Questions

- Is GitHub-generated PR-label release notes good enough, or do we need the tag action's commit-based changelog output?
- Is exact `checksums.txt` parity needed, or can we move to GitHub release asset digests?
- Is macOS notarization release-critical for this CLI, or best-effort as it is today?
- Should we use `anchore/quill` directly for closest GoReleaser parity, or `indygreg/apple-code-sign-action` for an action-wrapped `rcodesign` workflow?
- Should release metadata/versioning later move to Python Semantic Release or another semantic-release tool?

## References

- `python-semantic-release`: https://python-semantic-release.readthedocs.io/
- `wangyoucao577/go-release-action`: https://github.com/wangyoucao577/go-release-action
- `chihqiang/gobuild-action`: https://github.com/marketplace/actions/go-multi-platform-build
- `crazy-max/ghaction-xgo`: https://github.com/crazy-max/ghaction-xgo
- `softprops/action-gh-release`: https://github.com/softprops/action-gh-release
- `ncipollo/release-action`: https://github.com/ncipollo/release-action
- `actions/setup-go`: https://github.com/actions/setup-go
- `actions/upload-artifact`: https://github.com/actions/upload-artifact
- `actions/download-artifact`: https://github.com/actions/download-artifact
- GitHub generated release notes: https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes
- GitHub release asset digests: https://github.blog/changelog/2025-06-03-releases-now-expose-digests-for-release-assets/
- GoReleaser macOS notarization modes: https://goreleaser.com/customization/sign/notarize/
- `anchore/quill`: https://github.com/anchore/quill
- `indygreg/apple-code-sign-action`: https://github.com/indygreg/apple-code-sign-action
- `apple-actions/import-codesign-certs`: https://github.com/apple-actions/import-codesign-certs
- npm trusted publishing: https://docs.npmjs.com/trusted-publishers/
- `actions/setup-node`: https://github.com/actions/setup-node
