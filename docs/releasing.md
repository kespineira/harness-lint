# Release and publishing

The stable release workflow runs from a pushed annotated tag. It creates a
draft GitHub Release, publishes the Homebrew cask, signs and verifies the
checksum manifest, attaches the Cosign bundle, and only then publishes the
release. The release contract is intentionally fix-forward: a tag and its
published artifacts are never reused or replaced.

## Prerequisites

Before creating a release, confirm all of the following:

- The release commit is on `main`, has a clean tree, and passes the local CI
  commands below.
- Go 1.24.x is available locally for the equivalent checks.
- The repository's GitHub **Immutable Releases** setting is enabled.
- The `HOMEBREW_TAP_TOKEN` Actions secret exists. It must be a fine-grained
  personal access token scoped only to the `kespineira/homebrew-tap`
  repository with `Contents: Read and write`; grant no unrelated repository
  access or permissions. Store it only as the repository Actions secret.
- The workflow has the required `contents: write` and `id-token: write`
  permissions. The release job uses the repository `GITHUB_TOKEN` for the
  GitHub Release and its OIDC identity for keyless signing.

GoReleaser is pinned to **v2.17.1** and Cosign is pinned to **v3.1.3** in the
workflow. Do not silently upgrade either tool as part of a release; review a
version change as a release-automation change.

The current GoReleaser/Homebrew Cask integration cannot carry a Cask
`license` stanza in this setup, even though the GoReleaser configuration
identifies Apache-2.0. This is a limitation of generated Homebrew packaging
metadata, not a missing project license: `LICENSE` remains canonical and is
included in release archives and Linux packages.

## Tag validation jobs

The tag-triggered workflow runs three artifact-validation jobs before the
stable release job can publish anything:

- `release-e2e-linux` builds a snapshot on Linux, executes the matching Linux
  artifact on that runner, and inspects every other archive's structure and
  architecture without executing a non-native binary. It also validates Linux
  package metadata and payloads where the runner has the relevant package
  inspection tools.
- `release-e2e-macos` performs the equivalent native execution and
  non-native archive inspection on macOS.
- `release-homebrew-e2e` generates the cask on macOS, then runs Homebrew
  style, audit, and load checks in a temporary hosted-runner tap. The tap is
  removed on exit and is never pushed or applied to a local user's Homebrew
  installation.

These are validation levels, not claims that every cross-platform binary was
executed: only the artifact matching the current runner's OS and architecture
is run. The stable release job depends on all three jobs, then performs its
own source and policy quality gates before the first publication step.

## Exact pre-publication gates

The workflow intentionally fails before publishing when any gate fails:

1. In the stable `release` job, `HOMEBREW_TAP_TOKEN` must be non-empty. The
   token preflight runs before that job checks out the source or reaches any
   publication step; a missing secret stops the job with an error and creates
   no release or Homebrew commit. The three E2E jobs intentionally check out
   first with only `contents: read`, and never receive the tap token, because
   they must build and inspect artifacts.
2. The pushed ref must match `vX.Y.Z`, where each component is a non-negative
   decimal integer without leading zeroes (except zero itself).
3. The ref must name an annotated tag (`git cat-file -t` must be `tag`), its
   peeled commit must equal the checked-out `HEAD`, and that commit must be
   reachable from `origin/main`.
4. The GitHub API must establish that no Release already exists for the tag:
   HTTP 404 passes; HTTP 200 or any other response fails closed.
5. The job installs Go 1.24.x, Cosign v3.1.3, and GoReleaser v2.17.1.
6. Source and release-policy checks must pass: `git diff --check`,
   `./scripts/release-workflow-test.sh`, `gofmt -l .` must be empty,
   `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`,
   and `go build -trimpath -o <temporary-path> ./cmd/harness-lint`.
7. `./scripts/install_test.sh` and
   `./scripts/isolated-smoke.sh <built-binary>` must pass.
8. `goreleaser check` must validate `.goreleaser.yml`.

These gates run on the tag commit, not on an untagged local checkout. Run the
same commands locally before tagging so a failed workflow is exceptional.

## Exact local pre-tag commands

From a clean checkout of the release commit, run these commands in order. The
workflow policy test is deliberately cheap and deterministic: it parses only
the checked-in workflow and GoReleaser policy text and does not invoke
Homebrew or a network-facing command.

```sh
git status --short
git diff --check
test -z "$(gofmt -l .)"
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
release_binary="$(mktemp "${TMPDIR:-/tmp}/harness-lint.XXXXXX")"
go build -trimpath -o "$release_binary" ./cmd/harness-lint
./scripts/install_test.sh
./scripts/release-workflow-test.sh
./scripts/isolated-smoke.sh "$release_binary"
goreleaser check
goreleaser release --snapshot --clean
./scripts/release-e2e.sh
find dist -maxdepth 1 -type f -print | sort
tar -tzf dist/harness-lint_*_darwin_*.tar.gz
tar -tzf dist/harness-lint_*_linux_*.tar.gz
```

The final inspection should confirm the four archives, six Linux packages,
`checksums.txt`, and the generated cask. Inspect Linux package metadata and
payloads with `dpkg-deb --info/--contents`, `rpm -qp/--qf`, and
`apk manifest`/payload listing when those tools are available; inspect the
cask with `ruby -c`. `./scripts/release-e2e.sh` performs the deterministic checksum,
archive, package, cask, and matching-native installer checks, so the commands
above make the artifact inspection explicit without pretending to execute
non-native artifacts or modify a user's Homebrew installation.

## Draft, tap, sign, verify, publish

After all gates pass, the workflow performs this exact sequence:

1. GoReleaser `release --clean` creates a **draft** GitHub Release and
   publishes the Homebrew cask commit to `kespineira/homebrew-tap` using
   `HOMEBREW_TAP_TOKEN`.
2. Cosign v3.1.3 signs `dist/checksums.txt` and writes the current Cosign v3
   bundle to `dist/checksums.txt.sigstore.json`:

   ```sh
   cosign sign-blob --yes --bundle dist/checksums.txt.sigstore.json dist/checksums.txt
   ```

3. The workflow verifies the same manifest and bundle before upload. For
   release tag `vX.Y.Z`, the certificate identity is exactly:

   ```text
   https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z
   ```

   and the OIDC issuer is exactly
   `https://token.actions.githubusercontent.com`. The verification command
   is:

   ```sh
   cosign verify-blob dist/checksums.txt \
     --bundle dist/checksums.txt.sigstore.json \
     --certificate-identity "https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

4. The verified `checksums.txt.sigstore.json` is uploaded to the draft
   release.
5. The draft is changed to published/latest. A failed signing, verification,
   upload, or publish step leaves the release unpublished for investigation;
   do not rerun by reusing the tag. Fix forward with a new version.

Because GoReleaser updates the tap in step 1, a later signing or verification
failure can leave a Homebrew cask commit that references the still-draft
release while the GitHub Release remains unpublished. This partial outcome is
detectable by inspecting the tap commit and draft release; it is not an
all-or-nothing guarantee. Investigate and fix forward with a new version
instead of reusing the tag.

The installer verifies the selected archive against its unique SHA-256 line
in `checksums.txt`. When Cosign is installed, it also requires and verifies
the bundle with the identity above; a missing bundle or failed verification
prevents installation. Without Cosign, only the SHA-256 check is available.

## First release: `v0.1.0`

1. Merge the release-ready commit to `main`, confirm the CI workflow is green,
   and run the exact gates locally.
2. Enable GitHub Immutable Releases and create the fine-grained PAT described
   above. Add it as the `HOMEBREW_TAP_TOKEN` Actions secret and verify that it
   can write only to `kespineira/homebrew-tap`.
3. Create an annotated tag on the merged `main` commit:

   ```sh
   git switch main
   git pull --ff-only origin main
   git tag -a v0.1.0 -m "harness-lint v0.1.0"
   git push origin v0.1.0
   ```

4. Watch the `Release` workflow through its gates and draft/tap/sign/verify/
   publish stages. Confirm the draft contains archives, Linux packages,
   `checksums.txt`, and `checksums.txt.sigstore.json`; confirm the cask commit
   landed in the tap; then confirm the release is published as the latest.
5. Test one pinned archive install and, where available, one Cosign-verified
   install. Record the published release URL in the release notes.

If any step fails, diagnose it and make a new commit/tag (for example,
`v0.1.1`). Never delete, move, or recreate `v0.1.0`.
