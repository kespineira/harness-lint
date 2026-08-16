# Release architecture

This document is the release artifact and integrity contract for
`harness-lint`. The implementation lives in `.goreleaser.yml` and
`.github/workflows/release.yml`; changes to either must preserve the
invariants below.

## Version source and build metadata

Stable releases are annotated tags matching `vX.Y.Z`. The tag is the source
of the semantic version; the binary reports that version without the leading
`v`, the source commit, and the build date through `harness-lint version` (and
`--version`). Development and source builds use safe `0.0.0-dev`, `unknown`,
and `unknown` defaults. A `go install` build may recover a non-development
module version and VCS revision/time from `runtime/debug.BuildInfo`; empty or
development values are ignored. No generated version file is required.

Release builds set these linker values:

```text
-X github.com/kespineira/harness-lint/internal/version.Version={{.Version}}
-X github.com/kespineira/harness-lint/internal/version.Commit={{.Commit}}
-X github.com/kespineira/harness-lint/internal/version.BuildDate={{.Date}}
```

## Supported targets and artifacts

- Supported release targets are macOS and Linux on amd64 and arm64 only.
- The binary is named `harness-lint`.
- Archives are named `harness-lint_<version>_<os>_<arch>.tar.gz`, where
  `<version>` has no leading `v`.
- Every release includes `checksums.txt`, containing SHA-256 entries for the
  release artifacts.
- Linux packages are `.deb`, `.rpm`, and `.apk`; they install the binary as
  `/usr/bin/harness-lint` and do not install a daemon.
- Homebrew publishes the macOS cask to `kespineira/homebrew-tap`.
- GitHub Release artifacts are canonical for distribution and verification.

GoReleaser retains `CGO_ENABLED=0`; the embedded pure-Go SQLite driver needs
no C toolchain. The release archive includes `LICENSE` and `README.md`.

## Checksum authenticity

The release workflow signs `dist/checksums.txt` with Cosign v3.1.3 and
uploads the current Cosign v3 bundle as
`checksums.txt.sigstore.json`. Consumers that have Cosign installed should
verify both the archive's SHA-256 entry and the checksum bundle. The exact
keyless identity for the canonical repository is:

```text
https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z
```

The required OIDC issuer is:

```text
https://token.actions.githubusercontent.com
```

Equivalent verification for release `vX.Y.Z` is:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The curl installer performs this same verification when `cosign` is present;
missing or invalid bundle data then fails closed. Without Cosign it still
requires the archive's SHA-256 check and reports that authenticity was not
verified.

## Immutability and fix-forward

GitHub's **Immutable Releases** repository setting must be enabled. A
published release, its tag, and its artifacts are treated as immutable even
if a repository setting or provider behavior would otherwise permit edits.
The workflow rejects an existing release for the tag, and release policy
forbids replacing artifacts or reusing a tag. If a release is wrong, correct
the issue in a new commit and publish a new version (fix forward); never
delete and recreate `vX.Y.Z`.

The publication sequence is not transactional across GitHub and the tap:
after GoReleaser updates the tap, a later signing or verification failure can
leave a cask commit referencing the draft while the GitHub Release remains
unpublished. This is detectable and requires investigation and fix-forward;
it is not an all-or-nothing guarantee.

The complete operational sequence, required credentials, and first-release
checklist are in [Release and publishing](releasing.md).
