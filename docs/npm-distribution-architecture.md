# npm distribution architecture (ADR)

**Status:** accepted

**Date:** 2026-08-16

**Baseline:** `5abea42`

**Scope:** npm distribution of the existing Go CLI, including the accepted
tag-triggered release workflow and npm package manifests.

This ADR records the accepted workflow contract, not a publication result; no
successful npm registry publication is asserted here.

## Current fix-forward status

The `v0.1.2` tag's release run `32030039779` created canonical GitHub draft
assets and Homebrew cask state but failed in `npm-publish` before invoking npm.
The prior `actions/setup-node` configuration used `registry-url` and exported
a placeholder `NODE_AUTH_TOKEN=XXXXX...`; the publisher correctly rejected
that token-auth environment. All five public `0.1.2` npm endpoints remain
unpublished (HTTP 404), so `v0.1.2` and its draft are immutable history, not a
release to retry or edit. `v0.1.3` is the first intended stable npm/npx
release and must use a new fix-forward tag.

The accepted contract remains bootstrap-aware, exact-tag-derived, immutable,
native-first/root-last, Trusted Publishing/OIDC with provenance, and strictly
free of `NPM_TOKEN` and `NODE_AUTH_TOKEN`. Homebrew and `install.sh` continue
to distribute the native binary independently of npm.

## Decision

The accepted implementation publishes one small cross-platform launcher and
four native implementation packages after the one-time bootstrap confirms
ownership and publishability:

| Package | Node selector | GoReleaser archive member |
| --- | --- | --- |
| `harness-lint` | launcher; selects at runtime | — |
| `@kespineira/harness-lint-darwin-arm64` | `darwin` / `arm64` | `harness-lint_<version>_darwin_arm64.tar.gz` |
| `@kespineira/harness-lint-darwin-x64` | `darwin` / `x64` | `harness-lint_<version>_darwin_amd64.tar.gz` |
| `@kespineira/harness-lint-linux-arm64` | `linux` / `arm64` | `harness-lint_<version>_linux_arm64.tar.gz` |
| `@kespineira/harness-lint-linux-x64` | `linux` / `x64` | `harness-lint_<version>_linux_amd64.tar.gz` |

The root package declares all four platform packages as exact-version
`optionalDependencies`. Each platform package declares its matching npm
`os` and `cpu` values and contains only the staged `harness-lint` executable.
The root `bin` entry is a deterministic Node launcher: it maps
`process.platform` and `process.arch` to one of the four package names and
executes that already-installed binary. Node reports Intel 64-bit as `x64`,
whereas GoReleaser calls the same target `amd64`; this is the intentional
mapping above. Unsupported platforms fail with a clear message.

There are no `preinstall`, `install`, or `postinstall` hooks, no runtime
download, no shelling out to npm, and no curl-based fallback. Installation
must obtain the selected native package through npm's normal dependency
resolution; runtime execution only delegates to the binary already present
in `node_modules`.

## Version and artifact contract

The existing release workflow is `.github/workflows/release.yml`; its
annotated stable tag contract is `vX.Y.Z`. For the intended first stable npm
release tag `v0.1.3`, every one of the five package manifests has npm version
`0.1.3` (strip exactly the leading `v`; never publish `v0.1.3`). The root
package's optional dependency ranges are exact `0.1.3`, not a caret or a
dist-tag. A release is not ready until
all five package directories contain the same derived version.

GoReleaser remains the binary source of truth. In the release job, run the
pinned `.goreleaser.yml` build once with its normal clean output, then stage
the four archives from `dist/` into temporary npm package directories by
extracting only the matching archive member listed above. Do not rebuild the
binary with Node tooling, download a release asset during installation, or
recreate a binary outside GoReleaser. Validate each staged directory with
`npm pack --dry-run`, verify its package `os`/`cpu` selectors and executable
mode, and retain the existing checksum/signature gates for the Go release.

The npm package is a second distribution surface for the same release; it is
not a replacement for the canonical GitHub Release archives. The staging
step must fail closed if a required archive is absent, if the tag-derived
version does not match, or if an archive contains the wrong target.

The root name `harness-lint` remains conditional until bootstrap succeeds.
An unauthenticated registry HTTP 404 means that no public metadata was
returned for that request; it does not prove that the name is available,
unclaimed, publishable by this account, or secured against a conflicting
private/restricted record. Stop and revisit the names and npm scope if any
bootstrap publication or ownership check fails.

## Trusted publishing and provenance

Configure npm Trusted Publishing separately for each of the five package
names, after the package exists. For each configuration use:

```text
provider: GitHub Actions
organization/user: kespineira
repository: harness-lint
workflow filename: release.yml
```

The filename is exactly `release.yml`, not `.github/workflows/release.yml`.
The accepted workflow's separate npm publishing job explicitly grants
`id-token: write` and runs on a GitHub-hosted runner. The npm job also has
`contents: read`, Node >=22.14.0, and npm >=11.5.1. The publisher passes
`--registry https://registry.npmjs.org` explicitly and isolates npm
configuration; the workflow must not configure `registry-url`, `NPM_TOKEN`, or
`NODE_AUTH_TOKEN`.
Do not provide an `NPM_TOKEN` for normal releases. Trusted publishing uses a
short-lived OIDC credential; npm automatically generates provenance for a
public package published from this public repository. The implementation also
passes `npm publish --provenance` and verifies the resulting attestation with
`npm audit signatures --include-attestations`. Keep package `repository.url`
exactly equal (including case) to the GitHub repository URL.

Stable `v0.1.3` is the first intended OIDC npm release: all five packages must be published by
the configured Trusted Publishers, with npm's automatic provenance enabled.
Do not create or push the `v0.1.3` tag until all five names have been created
by bootstrap and all five trusted-publisher configurations have been saved
and checked. Normal release jobs must not define, read, or rely on an
`NPM_TOKEN` or any other long-lived npm publish token.

The account should restrict traditional token publishing after trust is
verified by selecting npm's “Require two-factor authentication and disallow
tokens” setting. Choose either direct publish permission or stage-only
permission deliberately. The recommendation for stable releases is direct
`npm publish` through Trusted Publishing, with a protected GitHub environment
if a human approval gate is wanted; this preserves npm's automatic
provenance for every package.

## Publication order and partial failure

After all existing release gates pass, the `release` job runs the pinned
GoReleaser release once. GoReleaser creates the GitHub Release draft and
updates Homebrew; the draft must remain unpublished while every subsequent
gate runs. The accepted workflow stages and packs npm inputs in that same job,
then the separate `npm-publish` job consumes the uploaded audited artifact.
The exact order is:

1. Sign `dist/checksums.txt`, verify the signature, and upload the verified
   `checksums.txt.sigstore.json` bundle to the still-draft GitHub Release.
2. Stage and validate all five npm package directories from the same
   canonical `dist/` output and tag-derived version. Run `npm pack --dry-run`,
   create the package tarballs, verify their contents, sizes, SHA-256 and
   integrity receipts, and upload the audited tarballs before any npm publish.
3. Publish the four native packages, in a fixed documented order, with
   `npm publish --access public --tag latest` through their Trusted Publishers
   and OIDC. The order is `darwin-arm64`, `darwin-x64`, `linux-arm64`, then
   `linux-x64`. Query the registry after each publish and require the exact
   name/version, `latest` dist-tag, tarball integrity, repository/platform
   metadata, and provenance record before continuing.
4. Publish `harness-lint` last with the same OIDC command. Query the registry
   and require the exact root version, `latest` tag, integrity, exact native
   optional dependencies, and provenance record.
5. Only after every preceding step succeeds, publish the stable GitHub
   Release draft. No step may set `draft=false` earlier.

There is no transaction spanning five npm packages, GitHub, and Homebrew. A
successful `name@version` is immutable and cannot be replaced, even after
unpublish; dist-tags are mutable pointers and are not a transaction either.
If a publish call returns a definite pre-acceptance error, verify its registry
record is still absent before an operational retry of only that package. If
the result is ambiguous, query the registry before retrying. If an immutable
version already exists, the publisher verifies it rather than republishing.
Never republish an accepted package/version, delete it to make a retry
possible, or retry when the registry's immutable package, metadata, integrity,
or provenance does not match the audited input. A mismatch is unsafe and
requires fix-forward.

Bootstrap has one observed npm registry exception: the first (and, for each
package, only) publication requested with `--tag bootstrap` may temporarily
also receive the `latest` dist-tag. This is not a desired state or a reason to
change publication order. An authenticated `npm dist-tag rm` used to remove
that temporary pointer may itself be rejected with HTTP 400; record the
registry response and continue through the documented gates rather than
retrying tag mutation, deleting an immutable package, or republishing it.

Several partial states are unavoidable: GoReleaser may have committed a
Homebrew cask that references the still-draft GitHub Release; a native npm
failure may leave some platform `latest` tags updated while the root
`latest` remains on the previous complete release; and a root npm success
may leave all npm packages live while the GitHub Release is still a draft if
the final GitHub publication fails. These states are detectable by checking
the tap commit, draft release, npm registry versions/tags, tarball integrity,
and provenance. They are not permission to replace content: after diagnosis,
use only the bounded failed-job continuation described below while the run is
still unpublished and all verification matches; otherwise fix forward with a
new coordinated commit, GitHub tag, and npm version. Never delete, move, or
recreate the affected GitHub release/tag or accepted npm `name@version`.

### Recovery and fix-forward

The split workflow permits a bounded GitHub Actions **Re-run failed jobs**
continuation when the same tagged run is still unpublished and the audited npm
tarballs were already uploaded. This is allowed only for a failed
`npm-publish` job after the canonical `release` job completed, or for the
failed final GitHub publication job after npm completed. The npm publisher is
resumable: it verifies every already-accepted immutable package against the
exact registry name/version, `latest` tag, metadata, tarball integrity, and
provenance, skips only a matching package, and continues in native order with
the root last using the already-uploaded tarballs. It never reruns GoReleaser
or stages a replacement artifact.

Do not re-run the full `release`/GoReleaser job, regenerate or replace audited
artifacts, generally reuse a tag, or retry a package when any registry
integrity, metadata, or provenance check fails. If the release is already
public, the audited inputs are unavailable, the draft/run state is no longer
coherent, or any verification mismatch is found, stop and fix forward with a
new coordinated commit, tag, and version.

### Staged publishing option

npm staged publishing can add a human review step: `npm stage publish` makes
a version unavailable publicly, then a maintainer reviews it and runs
`npm stage approve <stage-id>` with 2FA. Staging requires npm CLI >=11.15.0,
Node >=22.14.0, publish access, and an already-existing package. Stage all
five packages first, inspect all of them, then approve platform packages
before the root package. Staging and approval are still per-package, so
approval can partially complete; the same immutable-version and fix-forward
rules apply. Stage-only trusted-publisher permission is a valid higher-
assurance alternative, but it does not solve first-package bootstrap.

## Historical pre-`v0.1.2` bootstrap

This section preserves the one-time bootstrap history. It must not be used to
recreate or retry `v0.1.2`; the first intended stable npm/npx release is
`v0.1.3`, using a new immutable tag and the same bootstrap prerequisites.

The registry check on 2026-08-16T13:13Z used unauthenticated GET requests to
`https://registry.npmjs.org/<encoded-name>` and returned HTTP 404 for all five
names in the table. This means no public metadata was returned; it does not
guarantee that a name is available, unclaimed, publishable by this account,
or free of a private/restricted record. Confirm ownership and publishability
as part of bootstrap, and treat the root name as conditional until that check
and publication succeed.

npm's Trusted Publishing and staged publishing flows require an existing
package, so they cannot create the first registry record. Before the
historical `v0.1.2` tag, this one-time bootstrap procedure was required; the
next stable tag must be `v0.1.3`:

1. Prepare the release-ready commit without creating or pushing `v0.1.2`.
   Build one coherent GoReleaser snapshot with
   `goreleaser release --snapshot --clean --skip=publish`, and stage all four
   native npm packages from its canonical archives. Give all five temporary
   package manifests the same clearly non-release version, for example
   `0.0.0-bootstrap.1`; make the root's optional dependencies exact matches.
   Run the same package-content, `npm pack`, and hash checks intended for a
   stable release.
2. An authenticated maintainer publishes the four native packages and then
   the root package directly with npm 2FA, each using
   `npm publish --access public --tag bootstrap`. This is the official direct
   public-package path; `npm stage publish` cannot be used for a brand-new
   package. The requested `bootstrap` dist-tag is intentionally non-stable,
   but npm may temporarily also assign `latest` to this first publication;
   the temporary extra pointer must never be presented as the stable release.
3. Verify that all five package records and bootstrap versions are present,
   record any temporary `latest` tag, and confirm that the native tarballs
   contain the intended GoReleaser snapshot binaries. An authenticated
   `npm dist-tag rm` may be rejected with HTTP 400; treat that as observed
   registry behavior, not a reason to retry tag mutation or weaken the
   immutable/fix-forward policy. Configure one Trusted Publisher for each
   package using the exact `release.yml` filename, select `npm publish` as the
   allowed action, and confirm all five configurations are saved before
   proceeding.
4. Only after package existence, scope/name ownership, and all five trusted
   publishers are configured may the release-ready commit receive and push
   the annotated `v0.1.3` tag. The stable workflow then publishes all five
   packages through Trusted Publishing/OIDC with npm automatic provenance;
   it does not use `NPM_TOKEN` or any other long-lived npm token.

Bootstrap's human/2FA publication is intentionally not claimed as an npm
provenance release: npm provenance requires a supported cloud-hosted CI/CD
publisher and OIDC build identity. A canonical GoReleaser snapshot makes the
bootstrap native binaries reproducible and aligned with the project release,
but it does not create npm provenance attestations for those human-published
bootstrap package versions. The first intended stable `v0.1.3` packages are
the provenance-bearing OIDC publication. After that release, every package
must retain the durable dist-tag state `bootstrap=0.0.0-bootstrap.1` and
`latest=0.1.3`. The existing publisher registry verification enforces the
stable version and `latest=0.1.3`; it does not require or authorize removing
the bootstrap pointer. Do not commit or configure an `NPM_TOKEN` for normal
releases; if the maintainer later chooses another officially supported
bootstrap path, it must preserve the bootstrap tag and must not weaken the
no-token stable-release contract.

## Validation and implementation evidence

The accepted implementation adds deterministic local gates for release policy,
staging, packing, publisher security, and the npm consumer path:

```sh
./scripts/release-workflow-test.sh
./scripts/stage-npm-packages-test.sh
./scripts/pack-npm-packages-test.sh
./scripts/publish-npm-packages-test.sh
./scripts/npm-package-e2e-test.sh
```

On a GoReleaser snapshot with `dist/` available, run
`./scripts/npm-package-e2e.sh --dist dist`; it audits all five tarballs and
executes only the host-native package. The tag workflow runs this consumer
E2E on Linux and macOS before publication. After a real publish, the publisher
checks each public registry record and requires
`npm audit signatures --include-attestations` to show the matching provenance
attestation; the published-release consumer workflow separately validates the
public GitHub Release, installer, and Homebrew cask. These checks are evidence
requirements, not a claim that npm publication has already succeeded.

## External prerequisites and blockers

- An npm account that controls the `kespineira` scope and can publish all
  five packages publicly; scoped packages otherwise default to restricted,
  so every first publish must explicitly request public access.
- Two-factor authentication for the npm maintainer, especially if staged
  publishing or approval is selected.
- A public GitHub repository and exact `repository.url` in every manifest so
  npm can attach provenance to the intended source.
- GitHub-hosted runners, Node >=22.14.0, npm >=11.5.1 (and npm >=11.15.0
  when staging), `id-token: write`, and the exact trusted-publisher workflow
  filename `release.yml` configured independently on all five packages.
- An authenticated npm maintainer with 2FA for the one-time `bootstrap`-tagged
  publication. npm may temporarily assign `latest` to that first publication;
  no `NPM_TOKEN` is committed or configured for normal releases, and after
  bootstrap every stable publish uses Trusted Publishing/OIDC.
- A hard gate forbidding creation or push of `v0.1.3` until all five package
  names exist and all five trusted-publisher configurations are saved.
- The accepted implementation is in `.github/workflows/release.yml`,
  `npm/metadata.json`, the npm templates, and the staging/packing/publishing
  and E2E scripts; those files are the operational source of truth for this
  ADR.

## Evidence and official references

- [Trusted publishing for npm packages](https://docs.npmjs.com/trusted-publishers/):
  package existence/configuration fields, exact workflow filename,
  `id-token: write`, supported runners, automatic provenance, and token
  restriction.
- [npm trust](https://docs.npmjs.com/cli/v11/commands/npm-trust/): package
  existence and trusted-publisher configuration requirements.
- [Generating provenance statements](https://docs.npmjs.com/generating-provenance-statements/):
  public repository/package prerequisites, cloud-hosted runner, explicit
  provenance bootstrap command, and OIDC permission.
- [Creating and publishing scoped public packages](https://docs.npmjs.com/creating-and-publishing-scoped-public-packages/):
  scoped packages default to restricted and first public publication uses
  `--access public`.
- [Staged publishing for npm packages](https://docs.npmjs.com/staged-publishing/):
  stage/review/approve flow, package-exists prerequisite, CLI/Node versions,
  and 2FA approval.
- [npm unpublish policy](https://docs.npmjs.com/policies/unpublish/):
  immutable registry data and the rule that a used `name@version` cannot be
  reused, even after unpublish.
- [package.json fields](https://docs.npmjs.com/files/package.json/):
  npm package name/version requirements.
- [Adding dist-tags to packages](https://docs.npmjs.com/adding-dist-tags-to-packages/):
  `latest` behavior and explicit publish tags.
- Repository evidence: `.github/workflows/release.yml` (tag validation,
  `id-token: write`, GoReleaser and signing sequence), `.goreleaser.yml`
  (darwin/linux amd64/arm64 archives), and `docs/releasing.md` /
  `docs/release-architecture.md` (immutable GitHub release and fix-forward
  policy).
