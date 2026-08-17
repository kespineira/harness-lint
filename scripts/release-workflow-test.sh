#!/bin/sh
set -eu

# Deterministic, local-only policy checks for the release workflow.  This file
# intentionally does not invoke Homebrew or any network-facing command.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH=; cd -- "$script_dir/.." && pwd)
release_workflow=$project_root/.github/workflows/release.yml
ci_workflow=$project_root/.github/workflows/ci.yml
goreleaser_config=$project_root/.goreleaser.yml

fail() {
    echo "release workflow test: $*" >&2
    exit 1
}

[ -r "$release_workflow" ] || fail 'release workflow is missing'
[ -r "$ci_workflow" ] || fail 'CI workflow is missing'
[ -r "$goreleaser_config" ] || fail 'GoReleaser configuration is missing'
publish_script=$project_root/scripts/publish-npm-packages.py
[ -r "$publish_script" ] || fail 'npm publish script is missing'

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
    printf '%s\n' "$e2e_block" | grep -Fq "node-version: '22.14.0'" ||
        fail "$e2e_job does not pin the reviewed Node.js version"
    printf '%s\n' "$e2e_block" | grep -Fq 'npm install --global npm@11.5.1' ||
        fail "$e2e_job does not install the reviewed npm version"
    printf '%s\n' "$e2e_block" | grep -Fq './scripts/npm-package-e2e.sh --dist dist' ||
        fail "$e2e_job does not run the real npm package E2E"
done
homebrew_block=$(job_block release-homebrew-e2e)
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

# Every referenced action is pinned to a full immutable commit, and each
# release action is locked to the reviewed commit, including CI and all
# release jobs.
bad_pins=$(grep -hE '^[[:space:]]*uses:' "$release_workflow" "$ci_workflow" |
    grep -Ev '@[0-9a-f]{40}([[:space:]]|#|$)' || true)
[ -z "$bad_pins" ] || fail "an action is not pinned to a full commit: $bad_pins"
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
assert_action_pin actions/setup-node 48b55a011bda9f5d6aeb4c2d9c7362e8dae4041e
assert_action_pin goreleaser/goreleaser-action f06c13b6b1a9625abc9e6e439d9c05a8f2190e94
assert_action_pin sigstore/cosign-installer 6f9f17788090df1f26f669e9d70d6ae9567deba6
assert_action_pin actions/upload-artifact ea165f8d65b6e75b540449e92b4886f43607fa02
assert_action_pin actions/download-artifact d3f86a106a0bac45b974a628896c90dbdf5c8093

# Normal CI uses exact reviewed Node/npm versions and runs every deterministic
# local package gate; no registry credential may enter either workflow.
grep -Fq "node-version: '22.14.0'" "$ci_workflow" || fail 'CI Node.js version is not exact'
grep -Fq 'npm install --global npm@11.5.1' "$ci_workflow" || fail 'CI npm version is not exact'
for gate in './scripts/isolated-smoke.sh' './scripts/stage-npm-packages-test.sh' \
    './scripts/pack-npm-packages-test.sh' './scripts/publish-npm-packages-test.sh' \
    './scripts/npm-package-e2e-test.sh'; do
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
npm_block=$(job_block npm-publish)
printf '%s\n' "$npm_block" | grep -Eq '^    needs: release$' ||
    fail 'npm publish job is not gated on canonical release job'
printf '%s\n' "$npm_block" | grep -Eq '^      id-token: write$' ||
    fail 'npm publish job cannot use OIDC'
printf '%s\n' "$npm_block" | grep -Fq 'Download audited npm publication inputs' ||
    fail 'npm publish job does not download audited inputs'
printf '%s\n' "$npm_block" | grep -Fq 'uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' ||
    fail 'npm publish job does not checkout with the reviewed action'
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
