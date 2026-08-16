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
printf '%s\n' "$release_block" | grep -Eq '^    permissions:$' ||
    fail 'release job does not declare publish permissions'
printf '%s\n' "$release_block" | grep -Eq '^      contents: write$' ||
    fail 'release job cannot publish repository contents'
printf '%s\n' "$release_block" | grep -Eq '^      id-token: write$' ||
    fail 'release job cannot obtain an OIDC identity token'

# Every referenced action is pinned to a full immutable commit.  Checkout is
# additionally locked to the reviewed commit, including CI and all release jobs.
bad_pins=$(grep -hE '^[[:space:]]*uses:' "$release_workflow" "$ci_workflow" |
    grep -Ev '@[0-9a-f]{40}([[:space:]]|#|$)' || true)
[ -z "$bad_pins" ] || fail "an action is not pinned to a full commit: $bad_pins"
checkout_hashes=$(grep -hE '^[[:space:]]*uses:[[:space:]]*actions/checkout@' "$release_workflow" "$ci_workflow" |
    sed -E 's/.*@([0-9a-f]{40}).*/\1/')
[ -n "$checkout_hashes" ] || fail 'no checkout action was found'
while IFS= read -r checkout_hash; do
    [ "$checkout_hash" = 3d3c42e5aac5ba805825da76410c181273ba90b1 ] ||
        fail "checkout action is not pinned to the reviewed commit: $checkout_hash"
done <<EOF_CHECKOUT
$checkout_hashes
EOF_CHECKOUT

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

echo 'release workflow policy checks passed'
