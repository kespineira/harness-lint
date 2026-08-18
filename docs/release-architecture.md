# Release architecture

This document defines the release artifact and supply-chain contract for
`harness-lint`. The implementation lives in `.goreleaser.yml`,
`.github/workflows/release.yml`, and the release verification scripts; changes
to those files must preserve these invariants.

## Version, build, and targets

Stable releases use annotated tags matching `vX.Y.Z`. The tag is the only
semantic-version source. The binary reports the version without `v`, the
source commit, and the build date through `harness-lint version` and
`--version`; development builds use safe unknown values.

Release builds set:

```text
-X github.com/kespineira/harness-lint/internal/version.Version={{.Version}}
-X github.com/kespineira/harness-lint/internal/version.Commit={{.Commit}}
-X github.com/kespineira/harness-lint/internal/version.BuildDate={{.Date}}
```

Supported release targets are macOS and Linux on amd64 and arm64. Every
release contains four archives named
`harness-lint_<version>_<os>_<arch>.tar.gz`, a SHA-256 `checksums.txt`, and
four matching SPDX 2.3 SBOM files. Linux also publishes `.deb`, `.rpm`, and
`.apk` packages installing `/usr/bin/harness-lint`; no package installs or
enables a daemon. Homebrew publishes the macOS cask to the configured tap.
GitHub Release assets are the canonical distribution surface.

GoReleaser keeps `CGO_ENABLED=0`; archives include `LICENSE` and `README.md`.
The release toolchain is pinned to Go **1.26.6**, Node.js **24.19.0** (Active
LTS), npm **12.0.2**, GoReleaser **2.17.1**, Cosign **3.1.3**, and Syft
**1.51.0**.

## Validation and promotion gates

The tag workflow first runs three read-only artifact validation jobs:

- Linux executes its matching native archive and inspects non-native archives
  and Linux package metadata.
- macOS executes its matching native archive and inspects non-native archives.
- Homebrew validates cask Ruby, style, audit, and load in a temporary hosted
  runner tap that is removed and never pushed.

All three jobs have only `contents: read`. The stable release job requires all
three, validates an annotated tag whose commit is reachable from `main`,
rejects an existing GitHub Release for the tag, checks the clean source and
release policy, and runs the Go, installer, package, and smoke-test gates.

The promotion sequence is deliberately monotonic:

1. GoReleaser creates a draft GitHub Release and updates Homebrew.
2. A read-only gate validates that the actual stable `dist/` contains exactly
   the four archives, four matching SPDX SBOMs, six Linux packages, and their
   fourteen unique, correct checksums.
3. Cosign signs and verifies `dist/checksums.txt`.
4. GitHub attests the checksum subject with SLSA build provenance and attests
   each archive with its matching SPDX 2.3 SBOM. Verification checks exact
   archive subjects, repository, workflow, tag, and predicate identities before
   npm staging.
5. The release job stages and audits all five npm tarballs from the same
   GoReleaser output, then uploads those immutable inputs.
6. The OIDC npm job publishes the four native packages in fixed order and the
   root launcher last, verifying exact metadata, integrity, `latest`, and npm
   provenance after each package.
7. The final job promotes the GitHub draft only after every npm gate succeeds.

There is no transaction across GitHub, Homebrew, and npm. A matching
immutable package/version may be verified and skipped on a bounded failed-job
continuation while the tagged run is unpublished and audited inputs remain
available. Any mismatch, missing input, or unsafe retry stops the run.

## Integrity and attestations

Cosign signs the checksum manifest with keyless GitHub OIDC. Consumers verify
the archive's unique SHA-256 line and then:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

For each archive, the build-provenance subject is the archive itself and the
SBOM-provenance subject is the same archive, with predicate types
`https://slsa.dev/provenance/v1` and `https://spdx.dev/Document/v2.3`.
`scripts/verify-release-attestations.sh` verifies all four archive subjects
against the exact repository, `release.yml` workflow, and `refs/tags/vX.Y.Z`
source ref. It also requires each generated SBOM to declare `SPDX-2.3`.

The npm publisher uses Trusted Publishing with short-lived GitHub OIDC,
requests `--provenance`, suppresses lifecycle scripts, and verifies each
public registry record with `npm audit signatures --include-attestations`.
Normal releases never use `NPM_TOKEN` or `NODE_AUTH_TOKEN`.

## Least privilege and action pins

The release workflow has top-level `permissions: {}` and declares permissions
per job. E2E jobs have only `contents: read`; the canonical release job has
`contents: write`, `id-token: write`, and `attestations: write`; npm publishing
has `contents: read` and `id-token: write`; final GitHub promotion has only
`contents: write`. The Homebrew token is a separately scoped repository secret.

Every referenced GitHub Action in CI and release workflows is pinned to a
full immutable commit SHA with a version comment. The local
`scripts/release-workflow-test.sh` policy gate checks the full-SHA rule, exact
tool pins, permissions, artifact subjects, ordering, no-token rule, and
promotion dependencies.

## Immutability, dependencies, and fix-forward

GitHub **Immutable Releases** must be enabled. Tags, release assets, and
accepted npm `name@version` records are never deleted, replaced, or reused.
If a release is wrong or a gate finds an integrity, metadata, provenance, or
subject mismatch, correct the source in a new commit and publish a new version
(fix forward). Do not rerun GoReleaser to replace audited artifacts or retry
an accepted package.

Dependabot is configured for grouped GitHub Actions and Go module updates on a
weekly Monday schedule. Dependabot changes are reviewed and merged manually;
there is no auto-merge policy. Tool-pin, permission, action-SHA, or release
contract changes require the same release-policy review and tests.

Operational release steps and exact local commands are in
[Release and publishing](releasing.md). npm package-specific decisions are in
[npm distribution architecture](npm-distribution-architecture.md).
