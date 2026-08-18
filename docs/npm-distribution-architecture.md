# npm distribution architecture (ADR)

**Status:** accepted

**Scope:** npm distribution of the Go CLI and its tag-triggered release
workflow.

## Decision

The npm surface consists of one cross-platform launcher and four native
implementation packages:

| Package | Node selector | GoReleaser archive member |
| --- | --- | --- |
| `harness-lint` | launcher; selects at runtime | — |
| `@kespineira/harness-lint-darwin-arm64` | `darwin` / `arm64` | `harness-lint_<version>_darwin_arm64.tar.gz` |
| `@kespineira/harness-lint-darwin-x64` | `darwin` / `x64` | `harness-lint_<version>_darwin_amd64.tar.gz` |
| `@kespineira/harness-lint-linux-arm64` | `linux` / `arm64` | `harness-lint_<version>_linux_arm64.tar.gz` |
| `@kespineira/harness-lint-linux-x64` | `linux` / `x64` | `harness-lint_<version>_linux_amd64.tar.gz` |

The root package declares all four native packages as exact-version
`optionalDependencies`. The launcher maps `process.platform` and
`process.arch` to the already-installed executable. Unsupported platforms
fail clearly. No package has `preinstall`, `install`, or `postinstall` hooks;
there is no runtime download, shell-out to npm, or curl fallback.

The root consumer engine remains `node >=18.0.0` for launcher compatibility.
Node.js 18 is EOL; supported-LTS Node.js is recommended for new consumers.
The release workflow itself uses Node.js **24.19.0** (Active LTS) and npm
**12.0.2**. Runtime compatibility and release-toolchain versions are
deliberately separate contracts.

## Version and artifact contract

An annotated stable tag is `vX.Y.Z`; stripping exactly the leading `v` gives
the npm version `X.Y.Z`. All five manifests, optional dependencies, tarball
names, and registry records must use that exact version. The `v` prefix is
never published.

GoReleaser is the binary source of truth. The release job runs the pinned
GoReleaser build once, stages the four native packages from its `dist/`
archives, validates selectors and executable modes, and runs `npm pack` plus
content and hash audits. It never rebuilds a binary with Node tooling or
downloads a binary at install time. Missing archives, wrong targets, version
mismatches, or unexpected package files fail closed.

The fixed native-first/root-last order is:

1. `@kespineira/harness-lint-darwin-arm64`
2. `@kespineira/harness-lint-darwin-x64`
3. `@kespineira/harness-lint-linux-arm64`
4. `@kespineira/harness-lint-linux-x64`
5. `harness-lint`

## Trusted Publishing and provenance

Configure npm Trusted Publishing independently for every package:

```text
provider: GitHub Actions
organization/user: kespineira
repository: harness-lint
workflow filename: release.yml
```

The filename is exactly `release.yml`, not `.github/workflows/release.yml`.
The npm job runs on a GitHub-hosted runner with `contents: read` and
`id-token: write`, uses the public registry, and does not configure
`registry-url`, `NPM_TOKEN`, or `NODE_AUTH_TOKEN`. Trusted Publishing supplies
a short-lived OIDC credential; `npm publish --provenance` requests provenance
and `npm audit signatures --include-attestations` verifies the resulting
record.

After every package publish, the publisher requires exact name/version,
`latest` dist-tag, tarball integrity, repository metadata, platform metadata
for native packages, exact optional dependencies for the root, and a verified
SLSA provenance attestation. Only bounded registry propagation states may be
retried. Invalid records, metadata mismatches, integrity mismatches, or
missing provenance fail closed.

## Publication and promotion gates

The canonical release job first validates the tag and creates a draft GitHub
Release through GoReleaser. It signs and verifies `checksums.txt`, attests the
four archive subjects with build provenance and matching SPDX 2.3 SBOMs, and
verifies those attestations before staging npm packages. It uploads the
audited npm tarballs as immutable workflow inputs.

The separate OIDC npm job consumes those exact tarballs, publishes native
packages in fixed order, verifies each public record, then publishes the root
launcher. A final GitHub job promotes the draft only after all five npm
provenance gates succeed. The promotion is not transactional across npm,
Homebrew, and GitHub; partial external state is diagnosed rather than
replaced.

The package publisher is resumable only through a bounded failed-job
continuation while the tagged run is unpublished and audited inputs are
available. It verifies and skips an already accepted matching immutable
package, then continues in order. It never reruns GoReleaser or stages a
replacement artifact.

## Bootstrap and registry ownership

Trusted Publishing requires an existing package record. Before the first
stable package publication, an authorized maintainer must confirm control of
the package names and complete the one-time registry bootstrap using npm's
documented public-package process. Use a clearly non-release bootstrap version
and a non-stable dist-tag; do not represent that bootstrap package as the
stable release or as a provenance-bearing CI publication.

An unauthenticated registry `404` means only that no public metadata was
returned. It does not prove that a name is available, unclaimed, publishable,
or free of a private/restricted record. Stop the bootstrap if ownership or
scope checks fail. Once package records exist, configure and verify all five
Trusted Publishers before creating a stable tag.

## Immutability and fix-forward

An accepted npm `name@version` is immutable. Never delete it to make a retry
possible, republish it after acceptance, or alter its audited tarball. If the
registry record, integrity, metadata, provenance, or promotion state does not
match the audited input, stop and fix forward with a new coordinated commit,
tag, and npm version. Dependabot's weekly grouped updates are reviewed and
merged manually; release and tool-pin changes remain subject to the local
policy gates.

The operational sequence and exact commands are in
[Release and publishing](releasing.md); artifact verification is in
[Release architecture](release-architecture.md).
