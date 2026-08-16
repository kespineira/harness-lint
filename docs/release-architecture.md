# Release architecture

This document is the reviewed release contract for `harness-lint`. Release
automation must implement these invariants; this repository intentionally does
not add a `.goreleaser.yml` until that automation is reviewed separately.

## Version and build metadata

Tags named `vX.Y.Z` are the release version source. The binary reports the
semantic version without the tag's leading `v`, the source commit, and the
build date through `harness-lint version` (and `--version`). Development and
source builds use safe `0.0.0-dev`, `unknown`, and `unknown` defaults; a
`go install` build may recover a non-development module version and VCS
revision/time from `runtime/debug.BuildInfo`. Empty or development build-info
values (including a `(devel)` module version) are ignored, and the safe
defaults remain when metadata is unavailable; no generated version file is
required.

The linker targets for GoReleaser are:

```text
-X github.com/kespineira/harness-lint/internal/version.Version={{.Version}}
-X github.com/kespineira/harness-lint/internal/version.Commit={{.Commit}}
-X github.com/kespineira/harness-lint/internal/version.BuildDate={{.Date}}
```

## Artifact contract

- Supported targets are darwin and linux on amd64 and arm64 only.
- The binary name is `harness-lint`.
- Archives are named `harness-lint_<version-without-v>_<os>_<arch>.tar.gz`.
- The checksum file is named `checksums.txt`.
- Linux packages are deb, rpm, and apk.
- GitHub Release artifacts are canonical for distribution and verification.
- A keyless checksum-authenticity artifact may use the current Cosign v3
  bundle format.

`modernc.org/sqlite` is a pure-Go SQLite driver and is compatible with
`CGO_ENABLED=0`; the repository verifies this with `CGO_ENABLED=0 go test ./...`.
Release builds should therefore retain `CGO_ENABLED=0` and need no C toolchain.

## Release policy

Published release versions are immutable. If an artifact or release note is
wrong, fix forward with a new version rather than replacing an existing GitHub
Release artifact or tag.
