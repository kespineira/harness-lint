# Release and publishing

Stable releases are tag-triggered and promoted only after the complete
artifact, integrity, provenance, npm, and consumer gates pass. The workflow
is `.github/workflows/release.yml`; the canonical binary builder is
GoReleaser and the release tag is an annotated `vX.Y.Z` tag.

## Current toolchain and policy

The workflow pins these reviewed versions:

| Tool | Pin |
| --- | --- |
| Go | `1.26.6` |
| Node.js | `24.19.0` (Active LTS) |
| npm | `12.0.2` |
| GoReleaser | `2.17.1` |
| Cosign | `3.1.3` |
| Syft | `1.51.0` |

Do not silently change a pin during a release. Review a pin update as release
automation, run the policy tests, and update this document with the workflow
contract.

Every `uses:` reference in CI and release workflows is pinned to a full
40-character commit SHA with a version comment. The release workflow has
top-level `permissions: {}` and uses least privilege per job:

- Linux/macOS/Homebrew validation: `contents: read` only.
- Canonical release: `contents: write`, `id-token: write`,
  `attestations: write`.
- npm publication: `contents: read`, `id-token: write`.
- Final GitHub promotion: `contents: write` only.

The Homebrew tap credential is a repository Actions secret scoped only to the
tap repository with `Contents: Read and write`; it grants no unrelated access.
Normal npm publication is OIDC-only and must not receive `NPM_TOKEN` or
`NODE_AUTH_TOKEN`.

Dependabot groups GitHub Actions and Go module updates and opens them weekly
on Monday. Updates are reviewed and merged manually; auto-merge is not
enabled. Release-tool and action-SHA changes require the same policy review.

## Prerequisites

Before tagging, confirm:

1. The release commit is on `main`, the tree is clean, and local checks pass.
2. GitHub **Immutable Releases** is enabled.
3. `HOMEBREW_TAP_TOKEN` exists with the narrow scope described above.
4. All five npm packages exist, their names are controlled by the publisher,
   and each has Trusted Publishing configured as:

   ```text
   provider: GitHub Actions
   organization/user: kespineira
   repository: harness-lint
   workflow filename: release.yml
   ```

   The filename is exactly `release.yml`, not `.github/workflows/release.yml`.
5. The npm publisher can use the public registry at
   `https://registry.npmjs.org`; no long-lived npm token is configured.

The consumer package engine remains `node >=18.0.0` for launcher
compatibility. Node.js 18 is EOL, so supported-LTS Node.js is recommended for
consumers. Release automation uses Node.js 24.19.0 Active LTS and npm 12.0.2.

## Local pre-tag verification

Run from a clean checkout of the release commit. The commands below are
deterministic policy and package checks; the snapshot commands inspect the
actual GoReleaser output without publishing it.

```sh
git status --short
git diff --check
test -z "$(gofmt -l .)"
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
release_tmp="$(mktemp -d "${TMPDIR:-/tmp}/harness-lint.XXXXXX")"
cleanup() { rm -rf "$release_tmp"; }
trap cleanup EXIT INT TERM
go build -trimpath -o "$release_tmp/harness-lint" ./cmd/harness-lint
./scripts/install_test.sh
./scripts/release-workflow-test.sh
./scripts/release-sbom-test.sh
./scripts/release-dist-test.sh
./scripts/verify-release-attestations-test.sh
./scripts/stage-npm-packages-test.sh
./scripts/pack-npm-packages-test.sh
./scripts/publish-npm-packages-test.sh
./scripts/npm-package-e2e-test.sh
./scripts/isolated-smoke.sh "$release_tmp/harness-lint"
goreleaser check
goreleaser release --snapshot --clean
./scripts/release-e2e.sh
./scripts/npm-package-e2e.sh --dist dist
./scripts/published-release-smoke-workflow-test.sh
```

Inspect the snapshot with `find dist -maxdepth 1 -type f -print | sort` and
`tar -tzf dist/*.tar.gz`. The expected distributable set is four archives,
four SPDX files, six Linux packages, `checksums.txt`, and the generated cask.
Only the host-native archive is executed; other targets are inspected for
structure and architecture. Where available, inspect packages with
`dpkg-deb`, `rpm`, and `apk`, and validate the cask with `ruby -c`.

## Workflow gates and promotion

The tag-triggered workflow runs Linux, macOS, and isolated Homebrew E2E jobs
with read-only permissions before the stable release job. The stable job then:

1. Requires the tap secret before checkout or mutation.
2. Validates a stable annotated `vX.Y.Z` tag, its peeled commit, ancestry from
   `origin/main`, and absence of an existing GitHub Release.
3. Installs and verifies the exact pinned toolchain and runs all source,
   release-policy, formatting, test, build, installer, and smoke gates.
4. Runs GoReleaser once to create a draft GitHub Release and generate the cask
   locally (the `homebrew_casks` upload is skipped), then runs the custom
   publisher exactly once. The publisher normalizes the generated file and
   uses the scoped tap Contents API to replace only a strict older managed
   cask, preserving the generated version, canonical URLs, and checksums while
   satisfying current Homebrew stanza ordering.
5. Validates the actual stable `dist/` artifact set, SPDX documents, and all
   fourteen checksums without rebuilding or executing it.
6. Signs and verifies `dist/checksums.txt` with Cosign.
7. Publishes one build-provenance attestation for the checksum subject and one
   SPDX 2.3 SBOM attestation for each of the four archive subjects. It verifies
   all subjects before npm staging.
8. Stages, packs, audits, and uploads all five npm tarballs from that exact
   GoReleaser output.

The separate npm job checks out the exact tagged commit, consumes only the
audited tarballs, and publishes native packages in fixed order followed by
the root launcher. It verifies exact package metadata, `latest`, integrity,
and provenance after every publication. The final job changes the draft to
stable/latest only after all five npm gates succeed.

## Checksum and attestation verification

For a downloaded archive and release `vX.Y.Z`, first verify its unique
checksum entry and then verify the Cosign bundle:

```sh
archive="harness-lint_X.Y.Z_linux_amd64.tar.gz"
grep "  $archive$" checksums.txt | sha256sum -c -
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The release workflow's keyless identity is the exact
`release.yml@refs/tags/vX.Y.Z` workflow identity and its issuer is
`https://token.actions.githubusercontent.com`. For every archive, verify the
SLSA build-provenance subject and matching SPDX 2.3 SBOM subject:

```sh
workflow=kespineira/harness-lint/.github/workflows/release.yml
gh attestation verify "$archive" \
  --repo kespineira/harness-lint \
  --signer-workflow "$workflow" \
  --source-ref refs/tags/vX.Y.Z \
  --cert-identity "https://github.com/$workflow@refs/tags/vX.Y.Z" \
  --predicate-type https://slsa.dev/provenance/v1

sbom="$archive.spdx.json"
jq -e '.spdxVersion == "SPDX-2.3" and .SPDXID == "SPDXRef-DOCUMENT"' "$sbom"
gh attestation verify "$archive" \
  --repo kespineira/harness-lint \
  --signer-workflow "$workflow" \
  --source-ref refs/tags/vX.Y.Z \
  --cert-identity "https://github.com/$workflow@refs/tags/vX.Y.Z" \
  --predicate-type https://spdx.dev/Document/v2.3
```

`scripts/verify-release-attestations.sh` performs this exact four-archive
verification in the release job. Each attestation must name the archive as
its subject; an SBOM is never treated as an attestation for a different
archive.

## npm provenance verification

Trusted Publishing uses GitHub OIDC and npm automatically records provenance
for public packages. To verify a consumer record for `harness-lint@X.Y.Z`:

```sh
npm_tmp="$(mktemp -d)"
trap 'rm -rf "$npm_tmp"' EXIT
cd "$npm_tmp"
npm init --yes >/dev/null
npm install --ignore-scripts --no-audit --no-fund --package-lock=true \
  --save-exact --registry https://registry.npmjs.org harness-lint@X.Y.Z
npm audit signatures --json --include-attestations \
  --registry https://registry.npmjs.org
```

The JSON result must show the exact package and version under `verified`, with
no `invalid` or `missing` records and a SLSA provenance attestation. The same
audit is required after every native package and after the root package in
the publisher. npm registry propagation may receive bounded retries only
when the exact record is not yet visible; a definite mismatch fails closed.

## Failure handling and fix-forward

GitHub Release tags/assets and accepted npm `name@version` records are
immutable. There is no transaction spanning GitHub, Homebrew, and npm, so a
failure can leave a draft, a tap commit, or earlier native npm packages. The
only permitted continuation is a bounded **Re-run failed jobs** for the same
unpublished tagged run when audited tarballs remain available. The publisher
verifies matching immutable packages, skips only exact matches, and continues
native order/root-last without rerunning GoReleaser.

Do not rerun the full release job, regenerate or replace audited artifacts,
delete an accepted package, reuse a tag, or retry through an integrity,
metadata, provenance, or subject mismatch. If continuation is unavailable or
unsafe, correct the source in a new commit and publish a new coordinated
version (fix forward). GitHub Immutable Releases must remain enabled.

For the complete package design, see [npm distribution architecture](npm-distribution-architecture.md).
