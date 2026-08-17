# Release and publishing

## Current fix-forward status

Release run `32030039779` for the annotated `v0.1.2` tag created the
canonical GitHub draft assets and Homebrew cask state, but `npm-publish`
failed before any npm package was published. `actions/setup-node` had been
configured with `registry-url`, which exported a placeholder
`NODE_AUTH_TOKEN=XXXXX...`; `scripts/publish-npm-packages.py` correctly
rejected that token-auth environment before invoking npm. All five public
`0.1.2` npm endpoints remain unpublished (HTTP 404), so the existing tag and
draft must not be reused or edited. `v0.1.3` is the first intended stable
npm/npx release and must be a new immutable fix-forward tag.

The fix-forward keeps the one-time bootstrap history, exact tag-derived
versioning, native-first/root-last order, Trusted Publishing/OIDC and
provenance checks, and the no-`NPM_TOKEN`/no-`NODE_AUTH_TOKEN` rule. The
Homebrew cask and `install.sh` remain native distribution paths and do not
change the npm publication status.

The stable release workflow runs from a pushed annotated tag. It creates a
draft GitHub Release, publishes the Homebrew cask, signs and verifies the
checksum manifest, stages and audits five npm packages, publishes those
packages through Trusted Publishing/OIDC, and only then publishes the GitHub
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
- All five npm package names and their per-package Trusted Publisher settings
  exist before the `v0.1.3` tag is created. Each setting is `GitHub Actions`,
  organization/user `kespineira`, repository `harness-lint`, and workflow
  filename exactly `release.yml` (not `.github/workflows/release.yml`). The
  npm publisher job has `contents: read` and `id-token: write` and uses the
  public registry at `https://registry.npmjs.org`.
- The one-time bootstrap has been completed by an authenticated human npm
  maintainer with 2FA, requesting the non-stable `bootstrap` dist-tag. npm
  may temporarily also assign `latest` to that first publication; this is an
  observed registry behavior, not a desired bootstrap state. Normal releases
  do not use `NPM_TOKEN` or `NODE_AUTH_TOKEN`.

GoReleaser is pinned to **v2.17.1**, Cosign is pinned to **v3.1.3**, Node.js is
pinned to **22.14.0**, and npm is pinned to **11.5.1** in the workflow. Do not
silently upgrade any of these tools as part of a release; review a version
change as a release-automation change.

The current GoReleaser/Homebrew Cask integration cannot carry a Cask
`license` stanza in this setup, even though the GoReleaser configuration
identifies Apache-2.0. This is a limitation of generated Homebrew packaging
metadata, not a missing project license: `LICENSE` remains canonical and is
included in release archives and Linux packages.

## npm package architecture and versioning

The npm distribution is five packages: the root launcher
`harness-lint`, plus `@kespineira/harness-lint-darwin-arm64`,
`@kespineira/harness-lint-darwin-x64`,
`@kespineira/harness-lint-linux-arm64`, and
`@kespineira/harness-lint-linux-x64`. The native packages are selected by npm
platform metadata; the root package's optional dependencies are exact matches
for the same release. The root launcher only delegates to the already
installed native executable and has no lifecycle hook or runtime download.

The stable tag is the only version source. Tag `v0.1.3` derives npm version
`0.1.3` by stripping exactly the leading `v`; all five manifests, tarball
filenames, native dependencies, and registry records must use that exact
version. The `v` is never published. The native publish order is fixed and the
root is last:

1. `@kespineira/harness-lint-darwin-arm64`
2. `@kespineira/harness-lint-darwin-x64`
3. `@kespineira/harness-lint-linux-arm64`
4. `@kespineira/harness-lint-linux-x64`
5. `harness-lint`

GoReleaser remains the canonical binary builder. The stable release job runs
the pinned GoReleaser release once, uses its `dist/` archives to stage the
four native npm packages, packs and audits all five tarballs, and uploads the
audited tarballs to the OIDC npm job. That job checks out the exact tagged
commit and downloads those inputs; it does not rebuild or restage a different
binary.

Configure Trusted Publishing separately on each package with provider
`GitHub Actions`, organization/user `kespineira`, repository `harness-lint`,
and workflow filename `release.yml`. npm's Trusted Publishing uses the
short-lived GitHub OIDC credential and automatically creates provenance for a
public package; the implementation also passes `--provenance` and verifies
the resulting attestation. After every publish, the workflow requires the
exact public registry version, `latest` tag, tarball integrity, repository and
platform metadata, and a successful `npm audit signatures
--include-attestations` provenance check before continuing. No normal release
uses a long-lived npm token.

## Tag validation jobs

The tag-triggered workflow runs three artifact-validation jobs before the
stable release job can publish anything:

- `release-e2e-linux` builds a snapshot on Linux, executes the matching Linux
  artifact on that runner, and inspects every other archive's structure and
  architecture without executing a non-native binary. It also validates Linux
  package metadata and payloads where the runner has the relevant package
  inspection tools, then runs the real npm package consumer E2E.
- `release-e2e-macos` performs the equivalent native execution and
  non-native archive inspection on macOS, then runs the real npm package
  consumer E2E.
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
5. The job installs Go 1.24.x, Node.js 22.14.0, npm 11.5.1, Cosign v3.1.3,
   and GoReleaser v2.17.1.
6. Source and release-policy checks must pass: `git diff --check`,
   `./scripts/release-workflow-test.sh`, `gofmt -l .` must be empty,
   `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`,
   and `go build -trimpath -o <temporary-path> ./cmd/harness-lint`.
7. `./scripts/install_test.sh` and
   `./scripts/isolated-smoke.sh <built-binary>` must pass.
8. `goreleaser check` must validate `.goreleaser.yml`.
9. The release job must stage and pack all five npm packages from the
   validated `dist/` output, and the npm publisher must consume those audited
   tarballs with OIDC before the GitHub draft can be published.

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
release_tmp="$(mktemp -d "${TMPDIR:-/tmp}/harness-lint.XXXXXX")"
cleanup() {
  rm -rf "$release_tmp"
}
trap cleanup EXIT
trap 'cleanup; exit 1' HUP INT TERM
release_binary="$release_tmp/harness-lint"
go build -trimpath -o "$release_binary" ./cmd/harness-lint
./scripts/install_test.sh
./scripts/release-workflow-test.sh
./scripts/stage-npm-packages-test.sh
./scripts/pack-npm-packages-test.sh
./scripts/publish-npm-packages-test.sh
./scripts/npm-package-e2e-test.sh
./scripts/isolated-smoke.sh "$release_binary"
goreleaser check
goreleaser release --snapshot --clean
./scripts/release-e2e.sh
./scripts/npm-package-e2e.sh --dist dist
find dist -maxdepth 1 -type f -print | sort
for archive in dist/*.tar.gz; do
  [ -f "$archive" ] || continue
  tar -tzf "$archive"
done
test -f dist/homebrew/Casks/harness-lint.rb
ls -l dist/homebrew/Casks/harness-lint.rb
```

The final inspection should confirm the four archives, six Linux packages,
`checksums.txt`, and the generated cask. Inspect Linux package metadata and
payloads with `dpkg-deb --info/--contents`, `rpm -qp/--qf`, and
`apk manifest`/payload listing when those tools are available; inspect the
cask with `ruby -c`. `./scripts/release-e2e.sh` performs the deterministic checksum,
archive, package, cask, and matching-native installer checks, so the commands
above make the artifact inspection explicit without pretending to execute
non-native artifacts or modify a user's Homebrew installation.

After a release is genuinely published, run the public-consumer validation
workflow (`published-release-smoke.yml`) or its local policy check,
`./scripts/published-release-smoke-workflow-test.sh`. It validates the public
GitHub release/tag, downloads the selected `install.sh`, and exercises the
published Linux installer and Homebrew cask in isolated environments. For
npm, validate each public record at the exact derived version with `npm view`
and require `npm audit signatures --include-attestations` to report the
matching provenance attestation. These are post-publication checks; a local
snapshot or an HTTP 404 probe is not evidence that npm publication succeeded.

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
5. The workflow derives npm version `X.Y.Z` from tag `vX.Y.Z`, stages the four
   native packages from the same GoReleaser `dist/` archives, packs and audits
   all five tarballs, and uploads those audited inputs to the npm publisher
   job. This is reuse of the canonical GoReleaser output, not a second binary
   build.
6. The npm publisher checks out the exact tagged commit, downloads the audited
   inputs, and publishes the native packages in the documented order, followed
   by `harness-lint` last, with Trusted Publishing/OIDC. npm automatically
   creates provenance for each public package; the publisher also requests
   `--provenance` and verifies each exact registry record with
   `npm audit signatures --include-attestations` before continuing.
7. Only after all five npm versions, `latest` tags, integrity records, and
   provenance attestations pass is the GitHub draft changed to
   published/latest. A failed `npm-publish` job may be continued with
   GitHub Actions **Re-run failed jobs** for the same unpublished tagged run,
   using the already-uploaded audited tarballs; the resumable publisher must
   verify matching immutable packages and continue native order/root-last
   without rerunning GoReleaser. If npm completed and only the final GitHub
   publication job failed, re-running that failed final job is permitted.
   Failures in the canonical `release`/GoReleaser job, missing audited inputs,
   or any registry integrity, metadata, or provenance mismatch are not safe to
   retry and require fix-forward.

The sequence is not transactional. Because GoReleaser updates the tap in step
1, a later failure can leave a Homebrew cask commit that references the
still-draft GitHub Release. A native npm failure can leave earlier native
`latest` tags updated, and a root npm success can leave all five packages live
while the GitHub Release remains a draft. These partial outcomes are
detectable by inspecting the tap commit, draft release, npm registry records,
integrity, and provenance. Never delete, replace, or republish an accepted
package/version, rerun the full release/GoReleaser job, or generally reuse a
tag. A bounded failed-job continuation is safe only while this tagged run is
still unpublished, the audited tarballs remain available, and every registry
verification matches; once the release is public or continuation is unsafe,
fix forward with a new coordinated version.

The installer verifies the selected archive against its unique SHA-256 line
in `checksums.txt`. When Cosign is installed, it also requires and verifies
the bundle with the identity above; a missing bundle or failed verification
prevents installation. Without Cosign, only the SHA-256 check is available.

## Historical one-time npm bootstrap before `v0.1.2`

This section preserves the original bootstrap history and must not be used to
recreate or retry the failed `v0.1.2` release. The next stable npm release is
`v0.1.3`, and it uses the same bootstrap prerequisites and immutable
fix-forward rules.

npm Trusted Publishing requires an existing package, so it cannot create the
first registry record. Before creating or pushing `v0.1.2`, an authenticated
human maintainer with npm 2FA must perform this one-time bootstrap:

1. Build a canonical GoReleaser snapshot and stage all four native packages.
   Give all five temporary manifests the same clearly non-release version,
   such as `0.0.0-bootstrap.1`, and run the package content, pack, and hash
   checks. Do not create or push `v0.1.2` yet.
2. Publish the four native packages, then the root package, directly with npm
   and 2FA using `--access public --tag bootstrap`. Request the `bootstrap`
   tag only; npm may nevertheless temporarily assign `latest` to the first
   (and, for each package, only) publication. This extra `latest` pointer is
   not desirable and must not be treated as a stable release. Do not describe
   this human bootstrap as a provenance-bearing stable release.
3. Verify all five package records and bootstrap versions, record any
   temporary `latest` pointer, and confirm that the native tarballs contain
   the intended GoReleaser snapshot binaries. An authenticated `npm dist-tag
   rm` can be rejected with HTTP 400; do not turn that rejection into a retry
   loop or a request to replace/delete an immutable package. Query and record
   the registry state, then continue with the ownership and Trusted Publisher
   gates.
4. Configure and save one Trusted Publisher for each package with provider
   `GitHub Actions`, organization/user `kespineira`, repository `harness-lint`,
   and workflow filename exactly `release.yml`. Confirm package existence,
   scope/name ownership, and publishability before creating and pushing the
   annotated `v0.1.2` tag.

The intended stable `v0.1.2` workflow was to publish all five packages through
OIDC Trusted Publishing with npm automatic provenance. That run instead
failed before npm publication at the placeholder-token gate described at the
top of this document. It did not publish any `0.1.2` npm package, and it must
not be reused. An unauthenticated registry
HTTP 404 only means that no public metadata was returned for that request; it
does not prove a name is available, unclaimed, publishable by this account,
or successfully published. No successful public npm registry publication is
claimed here; record the actual bootstrap and stable results from the registry
checks rather than inferring them.

After the future `v0.1.3` succeeds, the required durable dist-tag state for
every package is `bootstrap=0.0.0-bootstrap.1` and `latest=0.1.3`. The
publisher registry verification enforces the stable version and
`latest=0.1.3` before the release can continue; it does not require or
authorize cleanup of the bootstrap pointer. The temporary bootstrap `latest`
behavior and an HTTP 400 from an authenticated `npm dist-tag rm` are not
reasons to weaken the native-first/root-last order, Trusted Publishing/OIDC
provenance, immutable version policy, or stable-release gates.

## Release history and first-release record

`v0.1.0` and `v0.1.1` are existing Go releases and remain part of the
release history. Preserve the annotated `v0.1.1` tag and its published
artifacts; `v0.1.2` remains a failed, unpublished npm attempt and must not be
reused. The npm workflow starts its first intended stable release with the
separate `v0.1.3` version. Never delete, move, recreate, or retrofit npm
publication onto `v0.1.1` or `v0.1.2`.

### Historical first release: `v0.1.0`

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

For this historical release, a failed step was fixed in a new commit/tag (for
example, `v0.1.1`); preserve both tags and their artifacts. Never delete,
move, or recreate `v0.1.0` or `v0.1.1`.
