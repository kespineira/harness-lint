#!/bin/sh
set -eu

# Deterministic, local-only policy checks for the release workflow.  This file
# intentionally does not invoke Homebrew or any network-facing command.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH=; cd -- "$script_dir/.." && pwd)
release_workflow=$project_root/.github/workflows/release.yml
ci_workflow=$project_root/.github/workflows/ci.yml
goreleaser_config=$project_root/.goreleaser.yml
reviewed_node_version=24.19.0
reviewed_npm_version=12.0.2
reviewed_go_version=1.26.6
reviewed_syft_version=v1.51.0

fail() {
    echo "release workflow test: $*" >&2
    exit 1
}

[ -r "$release_workflow" ] || fail 'release workflow is missing'
[ -r "$ci_workflow" ] || fail 'CI workflow is missing'
[ -r "$goreleaser_config" ] || fail 'GoReleaser configuration is missing'
publish_script=$project_root/scripts/publish-npm-packages.py
[ -r "$publish_script" ] || fail 'npm publish script is missing'
attestation_script=$project_root/scripts/verify-release-attestations.sh
[ -r "$attestation_script" ] || fail 'release attestation verifier is missing'
release_dist_validator=$project_root/scripts/validate-release-dist.sh
[ -r "$release_dist_validator" ] || fail 'stable release dist validator is missing'
release_dist_validator_test=$project_root/scripts/release-dist-test.sh
[ -r "$release_dist_validator_test" ] || fail 'stable release dist validator test is missing'
homebrew_normalizer=$project_root/scripts/normalize_homebrew_cask.py
[ -r "$homebrew_normalizer" ] || fail 'Homebrew cask normalizer is missing'
homebrew_normalizer_test=$project_root/scripts/normalize_homebrew_cask_test.py
[ -r "$homebrew_normalizer_test" ] || fail 'Homebrew cask normalizer test is missing'
homebrew_publisher=$project_root/scripts/publish_homebrew_cask.py
[ -r "$homebrew_publisher" ] || fail 'Homebrew cask publisher is missing'
homebrew_publisher_test=$project_root/scripts/publish_homebrew_cask_test.py
[ -r "$homebrew_publisher_test" ] || fail 'Homebrew cask publisher test is missing'
PYTHONDONTWRITEBYTECODE=1 python3 "$homebrew_normalizer_test" || fail 'Homebrew cask normalizer tests failed'
PYTHONDONTWRITEBYTECODE=1 python3 "$homebrew_publisher_test" || fail 'Homebrew cask publisher tests failed'

# Keep this policy test itself as an explicit gate in both normal CI and the
# stable release job. This prevents a future workflow edit from silently
# dropping the deterministic checks while leaving the script unused.
grep -Fq 'run: ./scripts/release-workflow-test.sh' "$ci_workflow" ||
    fail 'normal CI has no release workflow policy gate'

# Release publication is tag-only and must not be manually dispatched.
grep -Eq '^  push:$' "$release_workflow" || fail 'release workflow is not push-triggered'
grep -Eq '^    tags:$' "$release_workflow" || fail 'release workflow has no tag filter'
grep -Eq "^      - 'v\\*'$" "$release_workflow" || fail 'release workflow tag filter is not v*'
if grep -Eq '^[[:space:]]+(workflow_dispatch|pull_request|branches):' "$release_workflow"; then
    fail 'release workflow has a non-tag trigger'
fi

# Do not grant release credentials to artifact-validation jobs.
grep -Eq '^permissions: \{\}$' "$release_workflow" || fail 'release workflow top-level permissions are not empty'

job_block() {
    job_name=$1
    awk -v wanted="  $job_name:" '
        $0 == wanted { in_job = 1; next }
        in_job && /^  [A-Za-z0-9_-]+:/ { exit }
        in_job { print }
    ' "$release_workflow"
}

for e2e_job in release-e2e-linux release-e2e-macos release-homebrew-e2e; do
    e2e_block=$(job_block "$e2e_job")
    printf '%s\n' "$e2e_block" | grep -Eq '^    permissions:$' ||
        fail "$e2e_job does not declare job permissions"
    printf '%s\n' "$e2e_block" | grep -Eq '^      contents: read$' ||
        fail "$e2e_job is not limited to contents: read"
done
for e2e_job in release-e2e-linux release-e2e-macos; do
    e2e_block=$(job_block "$e2e_job")
    printf '%s\n' "$e2e_block" | grep -Fq "go-version: '$reviewed_go_version'" ||
        fail "$e2e_job does not pin the exact reviewed Go version"
    printf '%s\n' "$e2e_block" | grep -Fq "node-version: '$reviewed_node_version'" ||
        fail "$e2e_job does not pin the reviewed Node.js version"
    printf '%s\n' "$e2e_block" | grep -Fq "syft-version: '$reviewed_syft_version'" ||
        fail "$e2e_job does not install the exact reviewed Syft version"
    printf '%s\n' "$e2e_block" | grep -Fq "npm install --global npm@$reviewed_npm_version" ||
        fail "$e2e_job does not install the reviewed npm version"
    printf '%s\n' "$e2e_block" | grep -Fq "test \"\$(npm --version)\" = '$reviewed_npm_version'" ||
        fail "$e2e_job does not verify the reviewed npm version"
    printf '%s\n' "$e2e_block" | grep -Fq './scripts/npm-package-e2e.sh --dist dist' ||
        fail "$e2e_job does not run the real npm package E2E"
done
homebrew_block=$(job_block release-homebrew-e2e)
homebrew_go_step=$(printf '%s\n' "$homebrew_block" | awk '
    $0 == "      - name: Set up Go" { in_step = 1; next }
    in_step && /^      - name:/ { exit }
    in_step { print }
')
printf '%s\n' "$homebrew_go_step" | grep -Fq 'uses: actions/setup-go@' ||
    fail 'Homebrew E2E does not use the reviewed setup-go action'
printf '%s\n' "$homebrew_go_step" | grep -Fq "go-version: '$reviewed_go_version'" ||
    fail 'Homebrew E2E does not pin the exact reviewed Go version'
printf '%s\n' "$homebrew_go_step" | grep -Eq '^        with:$' ||
    fail 'Homebrew E2E setup-go has no with block'
printf '%s\n' "$homebrew_go_step" | grep -Eq '^          cache: true$' ||
    fail 'Homebrew E2E setup-go cache is not enabled'
homebrew_syft_step=$(printf '%s\n' "$homebrew_block" | awk '
    $0 == "      - name: Install Syft" { in_step = 1; next }
    in_step && /^      - name:/ { exit }
    in_step { print }
')
printf '%s\n' "$homebrew_syft_step" | grep -Fq 'uses: anchore/sbom-action/download-syft@' ||
    fail 'Homebrew E2E does not use the reviewed Syft action'
printf '%s\n' "$homebrew_syft_step" | grep -Fq "syft-version: '$reviewed_syft_version'" ||
    fail 'Homebrew E2E does not install the exact reviewed Syft version'
syft_input_count=$(printf '%s\n' "$homebrew_syft_step" |
    awk '$0 ~ /^          [A-Za-z0-9_-]+:/ { count++ } END { print count + 0 }')
[ "$syft_input_count" -eq 1 ] ||
    fail 'Homebrew E2E Syft action has inputs beyond the reviewed syft-version'
printf '%s\n' "$homebrew_block" | grep -Fq "RELEASE_E2E_TAP: harness-lint/e2e-" ||
    fail 'Homebrew E2E tap is not runner-specific'
printf '%s\n' "$homebrew_block" | grep -Fq "HOMEBREW_NO_AUTO_UPDATE: '1'" ||
    fail 'Homebrew E2E does not disable auto-update'
printf '%s\n' "$homebrew_block" | grep -Fq 'trap cleanup EXIT INT TERM' ||
    fail 'Homebrew E2E has no signal-safe tap cleanup trap'
printf '%s\n' "$homebrew_block" | grep -Fq "brew tap-new --no-git \"\$tap_name\"" ||
    fail 'Homebrew E2E does not create its temporary tap locally'
printf '%s\n' "$homebrew_block" | grep -Fq "brew untap --force \"\$tap_name\"" ||
    fail 'Homebrew E2E does not remove its temporary tap'
if printf '%s\n' "$homebrew_block" | grep -Fq -- '--online'; then
    fail 'Homebrew E2E performs network-dependent audit against snapshot URLs'
fi
release_block=$(job_block release)
printf '%s\n' "$release_block" | grep -Fq "go-version: '$reviewed_go_version'" ||
    fail 'stable release job does not pin the exact reviewed Go version'
printf '%s\n' "$release_block" | grep -Fq "syft-version: '$reviewed_syft_version'" ||
    fail 'stable release job does not install the exact reviewed Syft version'
printf '%s\n' "$release_block" | grep -Fq "npm install --global npm@$reviewed_npm_version" ||
    fail 'stable release job does not install the reviewed npm version'
printf '%s\n' "$release_block" | grep -Fq "test \"\$(npm --version)\" = '$reviewed_npm_version'" ||
    fail 'stable release job does not verify the reviewed npm version'
printf '%s\n' "$release_block" | grep -Fq 'name: Run stable release quality gate' ||
    fail 'stable release job has no named quality gate'
printf '%s\n' "$release_block" | grep -Fq 'run: ./scripts/release-workflow-test.sh' ||
    fail 'stable release job does not run the release workflow policy gate'
printf '%s\n' "$release_block" | grep -Eq '^    permissions:$' ||
    fail 'release job does not declare publish permissions'
printf '%s\n' "$release_block" | grep -Eq '^      contents: write$' ||
    fail 'release job cannot publish repository contents'
printf '%s\n' "$release_block" | grep -Eq '^      id-token: write$' ||
    fail 'release job cannot obtain an OIDC identity token'
printf '%s\n' "$release_block" | grep -Eq '^      attestations: write$' ||
    fail 'release job cannot publish artifact attestations'

# Every referenced action is pinned to a full immutable commit, and each
# release action is locked to the reviewed commit, including CI and all
# release jobs.
bad_pins=$(grep -hE '^[[:space:]]*uses:' "$release_workflow" "$ci_workflow" |
    grep -Ev '@[0-9a-f]{40}([[:space:]]|#|$)' || true)
[ -z "$bad_pins" ] || fail "an action is not pinned to a full commit: $bad_pins"
missing_pin_comments=$(grep -hE '^[[:space:]]*uses:' "$release_workflow" "$ci_workflow" |
    grep -Ev '@[0-9a-f]{40}[[:space:]]+# v[0-9]' || true)
[ -z "$missing_pin_comments" ] || fail "an action pin is missing its version comment: $missing_pin_comments"
assert_action_pin() {
    action=$1
    expected=$2
    refs=$(grep -hE "^[[:space:]]*uses:[[:space:]]*${action}@" "$release_workflow" "$ci_workflow" || true)
    [ -n "$refs" ] || fail "no $action action was found"
    wrong_refs=$(printf '%s\n' "$refs" | grep -Ev "@${expected}([[:space:]]|#|$)" || true)
    [ -z "$wrong_refs" ] || fail "$action action is not pinned to the reviewed commit: $wrong_refs"
}
assert_action_pin actions/checkout 3d3c42e5aac5ba805825da76410c181273ba90b1
assert_action_pin actions/setup-go b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
assert_action_pin actions/setup-node 820762786026740c76f36085b0efc47a31fe5020
assert_action_pin goreleaser/goreleaser-action f06c13b6b1a9625abc9e6e439d9c05a8f2190e94
assert_action_pin sigstore/cosign-installer 6f9f17788090df1f26f669e9d70d6ae9567deba6
assert_action_pin anchore/sbom-action/download-syft e22c389904149dbc22b58101806040fa8d37a610
assert_action_pin actions/attest 1e69f48acb82d1966a394da916b4c1698aa569d6
assert_action_pin actions/upload-artifact 043fb46d1a93c77aae656e7c1c64a875d1fc6a0a
assert_action_pin actions/download-artifact 3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c

# Normal CI uses exact reviewed Node/npm versions and runs every deterministic
# local package gate; no registry credential may enter either workflow.
grep -Fq "go-version: '$reviewed_go_version'" "$ci_workflow" || fail 'CI Go version is not exact'
grep -Fq "node-version: '$reviewed_node_version'" "$ci_workflow" || fail 'CI Node.js version is not exact'
grep -Fq "npm install --global npm@$reviewed_npm_version" "$ci_workflow" || fail 'CI npm version is not exact'
for gate in './scripts/isolated-smoke.sh' './scripts/stage-npm-packages-test.sh' \
    './scripts/pack-npm-packages-test.sh' './scripts/publish-npm-packages-test.sh' \
    './scripts/npm-package-e2e-test.sh' './scripts/release-dist-test.sh'; do
    grep -Fq "$gate" "$ci_workflow" || fail "CI is missing deterministic gate $gate"
done
if grep -Eq 'NPM_TOKEN|NODE_AUTH_TOKEN' "$release_workflow" "$ci_workflow"; then
    fail 'release/CI workflow contains a long-lived npm token'
fi

# npm publish is OIDC-only, resumable, and verifies every immutable package
# before continuing. The explicit order is part of the release contract.
grep -Fq 'MAX_ATTEMPTS = 6' "$publish_script" || fail 'npm publish retry bound is missing'
grep -Fq '"--provenance"' "$publish_script" || fail 'npm publish lacks explicit provenance mode'
grep -Fq '"--ignore-scripts"' "$publish_script" || fail 'npm publish lacks script suppression'
grep -Fq '"--tag",' "$publish_script" || fail 'npm publish lacks explicit latest dist-tag'
grep -Fq '"--access",' "$publish_script" || fail 'npm publish lacks explicit public access'
grep -Fq 'npm audit' "$publish_script" || fail 'npm publish lacks npm CLI provenance audit'
grep -Fq '"--include-attestations"' "$publish_script" || fail 'npm provenance audit omits attestations'
grep -Fq 'NPM_CONFIG_USERCONFIG' "$publish_script" || fail 'npm publish does not isolate user config'
grep -Fq 'ACTIONS_ID_TOKEN_REQUEST_' "$publish_script" ||
    fail 'npm publish does not document preserving GitHub OIDC variables'
for native in darwin-arm64 darwin-x64 linux-arm64 linux-x64; do
    grep -Fq "harness-lint-$native" "$publish_script" || fail "native publish order omits $native"
done
grep -Fq 'PACKAGE_ORDER' "$publish_script" || fail 'npm publish package order is not explicit'
grep -Fq 'repository_identity' "$publish_script" || fail 'repository identity normalization is missing'
grep -Fq 'tarball SHA-256 receipt mismatch' "$publish_script" || fail 'tarball SHA-256 receipt gate is missing'
grep -Fq 'tarball integrity receipt mismatch' "$publish_script" || fail 'tarball SRI receipt gate is missing'

# GoReleaser must never replace an existing draft or artifact.  Keep these
# explicit false settings, and reject similarly named clobber/replacement knobs.
grep -Eq '^  replace_existing_draft: false$' "$goreleaser_config" ||
    fail 'replace_existing_draft is not explicitly false'
grep -Eq '^  replace_existing_artifacts: false$' "$goreleaser_config" ||
    fail 'replace_existing_artifacts is not explicitly false'
if grep -Eiq '^[[:space:]]*(clobber|replacement):[[:space:]]*(true|yes|on)$' \
    "$release_workflow" "$goreleaser_config"; then
    fail 'release configuration enables clobber or replacement'
fi

# GoReleaser generates the cask in the canonical dist directory but must not
# publish it: only the scoped Contents API publisher may mutate the tap.
grep -Eq '^    skip_upload: true$' "$goreleaser_config" ||
    fail 'Homebrew cask generation does not explicitly skip upload'
if grep -Eq '^[[:space:]]+token:.*HOMEBREW_TAP_TOKEN' "$goreleaser_config"; then
    fail 'GoReleaser still receives the Homebrew tap token'
fi
goreleaser_step=$(printf '%s\n' "$release_block" | awk '
    $0 == "      - name: Create draft release and generate Homebrew cask" { in_step = 1 }
    in_step && /^      - name:/ && $0 != "      - name: Create draft release and generate Homebrew cask" { exit }
    in_step { print }
')
printf '%s\n' "$goreleaser_step" | grep -Fq 'args: release --clean' ||
    fail 'canonical GoReleaser step does not create the draft release'
if printf '%s\n' "$goreleaser_step" | grep -Fq 'HOMEBREW_TAP_TOKEN'; then
    fail 'canonical GoReleaser step receives the Homebrew tap token'
fi
printf '%s\n' "$release_block" | grep -Fq 'Publish normalized Homebrew cask once' ||
    fail 'canonical job does not have a single custom cask publication step'

# Publication is gated on every artifact E2E job.
printf '%s\n' "$release_block" | grep -Eq '^    needs:$' ||
    fail 'release job has no E2E dependency list'
for e2e_job in release-e2e-linux release-e2e-macos release-homebrew-e2e; do
    printf '%s\n' "$release_block" | grep -Eq "^      - $e2e_job$" ||
        fail "release job does not depend on $e2e_job"
done

# The canonical job stages/packs before upload; a separate OIDC job can be
# rerun against the immutable audited tarballs without rebuilding Go artifacts.
printf '%s\n' "$release_block" | grep -Fq 'Stage npm packages from the validated tag version' ||
    fail 'canonical release job does not stage npm packages'
printf '%s\n' "$release_block" | grep -Fq 'Pack and audit all five npm tarballs' ||
    fail 'canonical release job does not pack/audit npm packages'
printf '%s\n' "$release_block" | grep -Fq 'Upload audited npm publication inputs' ||
    fail 'canonical release job does not upload audited npm inputs'
printf '%s\n' "$release_block" | grep -Fq 'path: dist/npm-packages' ||
    fail 'canonical release job does not upload the expected npm package directory'

# Validate the actual stable GoReleaser dist after its one release build and
# before any signing, attestation, or npm staging consumes it.
printf '%s\n' "$release_block" | grep -Fq 'Validate actual stable release output' ||
    fail 'canonical release job has no actual-dist validation step'
printf '%s\n' "$release_block" | grep -Fq 'Publish normalized Homebrew cask once' ||
    fail 'canonical release job does not publish the normalized Homebrew cask'
printf '%s\n' "$release_block" | grep -Fq 'python3 scripts/publish_homebrew_cask.py' ||
    fail 'canonical release job does not run the Homebrew cask publisher'
printf '%s\n' "$release_block" | grep -Fq -- '--dist-cask dist/homebrew/Casks/harness-lint.rb' ||
    fail 'Homebrew cask publisher does not consume the canonical dist cask'
if grep -Eiq 'echo[[:space:]].*(\$\{?HOMEBREW_TAP_TOKEN|\$HOMEBREW_TAP_TOKEN)|printf[[:space:]].*(\$\{?HOMEBREW_TAP_TOKEN|\$HOMEBREW_TAP_TOKEN)' \
    "$release_workflow"; then
    fail 'release workflow exposes the Homebrew tap token'
fi
# The literal GitHub expression is intentionally single-quoted for grep.
# shellcheck disable=SC2016
printf '%s\n' "$release_block" | grep -Fq 'run: ./scripts/validate-release-dist.sh --dist dist --version "$NPM_VERSION"' ||
    fail 'canonical release job does not run the actual-dist validator'
dist_validation_line=$(printf '%s\n' "$release_block" | grep -n 'validate-release-dist.sh' | cut -d: -f1)
cosign_line=$(printf '%s\n' "$release_block" | grep -n 'cosign sign-blob' | cut -d: -f1)
attest_line=$(printf '%s\n' "$release_block" | grep -n 'uses: actions/attest@' | cut -d: -f1 | sed -n '1p')
npm_stage_line=$(printf '%s\n' "$release_block" | grep -n 'stage-npm-packages.sh' | cut -d: -f1)
[ -n "$dist_validation_line" ] && [ -n "$cosign_line" ] && [ -n "$attest_line" ] && [ -n "$npm_stage_line" ] ||
    fail 'actual-dist validator/signing/attestation/npm steps are incomplete'
[ "$dist_validation_line" -lt "$cosign_line" ] &&
    [ "$dist_validation_line" -lt "$attest_line" ] &&
    [ "$dist_validation_line" -lt "$npm_stage_line" ] ||
    fail 'actual-dist validation does not precede signing, attestation, and npm staging'

# Attest the actual GoReleaser checksum subjects, then verify each canonical
# archive before any npm staging or publication can run.
printf '%s\n' "$release_block" | grep -Fq 'subject-checksums: dist/checksums.txt' ||
    fail 'canonical release job does not attest the GoReleaser checksum subjects'
attestation_step_count=$(printf '%s\n' "$release_block" | grep -c 'uses: actions/attest@' || true)
[ "$attestation_step_count" -eq 5 ] ||
    fail "canonical release job must have one provenance and four SBOM attestations (got $attestation_step_count)"
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
    printf '%s\n' "$release_block" | grep -Fq "subject-path: dist/harness-lint_\${{ steps.release_version.outputs.version }}_${target}.tar.gz" ||
        fail "canonical release job has no SBOM subject for $target"
    printf '%s\n' "$release_block" | grep -Fq "sbom-path: dist/harness-lint_\${{ steps.release_version.outputs.version }}_${target}.tar.gz.spdx.json" ||
        fail "canonical release job has no matching SBOM for $target"
done
printf '%s\n' "$release_block" | grep -Fq 'Verify archive attestations before npm staging' ||
    fail 'canonical release job does not verify attestations before npm staging'
verify_line=$(printf '%s\n' "$release_block" | grep -n 'verify-release-attestations.sh' | cut -d: -f1)
stage_line=$(printf '%s\n' "$release_block" | grep -n 'stage-npm-packages.sh' | cut -d: -f1)
[ -n "$verify_line" ] && [ -n "$stage_line" ] && [ "$verify_line" -lt "$stage_line" ] ||
    fail 'archive attestation verification does not precede npm staging'
for identity in '--repo' '--signer-workflow' '--source-ref' '--cert-identity' 'https://spdx.dev/Document/v2.3' 'https://slsa.dev/provenance/v1'; do
    grep -Fq -- "$identity" "$attestation_script" || fail "attestation verifier omits exact identity/predicate gate $identity"
done
npm_block=$(job_block npm-publish)
printf '%s\n' "$npm_block" | grep -Fq "npm install --global npm@$reviewed_npm_version" ||
    fail 'npm publish job does not install the reviewed npm version'
printf '%s\n' "$npm_block" | grep -Fq "test \"\$(npm --version)\" = '$reviewed_npm_version'" ||
    fail 'npm publish job does not verify the reviewed npm version'
printf '%s\n' "$npm_block" | grep -Eq '^    needs: release$' ||
    fail 'npm publish job is not gated on canonical release job'
printf '%s\n' "$npm_block" | grep -Eq '^      id-token: write$' ||
    fail 'npm publish job cannot use OIDC'
printf '%s\n' "$npm_block" | grep -Fq 'Download audited npm publication inputs' ||
    fail 'npm publish job does not download audited inputs'
printf '%s\n' "$npm_block" | grep -Fq 'uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' ||
    fail 'npm publish job does not checkout with the reviewed action'
# The literal GitHub expression must remain single-quoted for grep; it is not
# a shell expression despite shellcheck's generic interpolation warning.
# shellcheck disable=SC2016
printf '%s\n' "$npm_block" | grep -Fq 'ref: ${{ github.sha }}' ||
    fail 'npm publish job does not checkout the exact tagged commit'
printf '%s\n' "$npm_block" | grep -Fq 'persist-credentials: false' ||
    fail 'npm publish checkout retains credentials'
printf '%s\n' "$npm_block" | grep -Fq 'path: dist/npm-packages' ||
    fail 'npm artifact is not extracted into dist/npm-packages'
printf '%s\n' "$npm_block" | grep -Fq -- '--packages dist/npm-packages' ||
    fail 'npm publisher input does not match artifact extraction path'
printf '%s\n' "$npm_block" | grep -Fq 'publish-npm-packages.sh' ||
    fail 'npm publish job does not invoke the resumable publisher'
# setup-node's registry-url writes an npmrc and exports a NODE_AUTH_TOKEN,
# which would introduce token-auth state into this OIDC-only publisher. Keep
# the job free of both the registry-url setting and explicit token auth.
if printf '%s\n' "$npm_block" | grep -Eq 'registry-url:|NPM_TOKEN|NODE_AUTH_TOKEN|always-auth:'; then
    fail 'npm publish job configures registry-url or token authentication'
fi
npm_checkout_line=$(printf '%s\n' "$npm_block" | grep -n 'uses: actions/checkout@' | cut -d: -f1)
npm_download_line=$(printf '%s\n' "$npm_block" | grep -n 'uses: actions/download-artifact@' | cut -d: -f1)
npm_publish_line=$(printf '%s\n' "$npm_block" | grep -n 'publish-npm-packages.sh' | cut -d: -f1)
[ -n "$npm_checkout_line" ] && [ -n "$npm_download_line" ] && [ -n "$npm_publish_line" ] ||
    fail 'npm publish checkout/download/publish steps are incomplete'
[ "$npm_checkout_line" -lt "$npm_download_line" ] && [ "$npm_download_line" -lt "$npm_publish_line" ] ||
    fail 'npm publish does not checkout before download and repository script execution'
final_block=$(job_block publish-github-release)
printf '%s\n' "$final_block" | grep -Eq '^    needs: npm-publish$' ||
    fail 'GitHub stable publication is not gated on npm publication'
printf '%s\n' "$final_block" | grep -Fq -- '--draft=false --latest' ||
    fail 'GitHub stable publication command is missing'

echo 'release workflow policy checks passed'
