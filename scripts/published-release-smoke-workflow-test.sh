#!/bin/sh
set -eu

# Deterministic, local-only policy checks for the published consumer workflow.
# This test intentionally does not call GitHub, Homebrew, or any network API.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH=; cd -- "$script_dir/.." && pwd)
workflow=$project_root/.github/workflows/published-release-smoke.yml

fail() {
    echo "published release smoke workflow test: $*" >&2
    exit 1
}

[ -r "$workflow" ] || fail 'workflow is missing'

grep -Eq '^  release:$' "$workflow" || fail 'workflow has no release trigger'
grep -Eq '^    types:$' "$workflow" || fail 'release trigger has no event types'
grep -Eq '^      - published$' "$workflow" || fail 'release trigger is not published-only'
grep -Eq '^  workflow_dispatch:$' "$workflow" || fail 'workflow has no manual replay trigger'
grep -Eq '^      tag:$' "$workflow" || fail 'manual trigger has no explicit tag input'
grep -Eq '^        required: true$' "$workflow" || fail 'manual tag input is not required'
grep -Eq '^        type: string$' "$workflow" || fail 'manual tag input is not a string'

grep -Eq '^permissions:$' "$workflow" || fail 'workflow has no top-level permissions'
grep -Eq '^  contents: read$' "$workflow" || fail 'workflow is not limited to contents: read'
if grep -Eiq 'contents:[[:space:]]*write|id-token:[[:space:]]*write|packages:[[:space:]]*write|secrets\.' "$workflow"; then
    fail 'workflow requests write permissions or secrets'
fi

# Every action reference, if one is added later, must be an immutable commit.
bad_pins=$(grep -E '^[[:space:]]*uses:' "$workflow" | grep -Ev '@[0-9a-f]{40}([[:space:]]|#|$)' || true)
[ -z "$bad_pins" ] || fail "an action is not pinned to a full commit: $bad_pins"

grep -Fq 'github.event.release.tag_name' "$workflow" || fail 'release event tag is not used'
grep -Fq 'inputs.tag' "$workflow" || fail 'workflow dispatch tag is not used'
grep -Fq 'releases/tags/' "$workflow" || fail 'published release is not validated through the API'
grep -Fq 'git/ref/tags/' "$workflow" || fail 'tag ref is not validated through the API'
grep -Fq '.draft == false' "$workflow" || fail 'draft releases are not rejected'
grep -Fq '.published_at' "$workflow" || fail 'published timestamp is not validated'
grep -Fq 'checksums.txt' "$workflow" || fail 'release checksum asset is not validated or consumed'
grep -Fq 'scripts/install.sh' "$workflow" || fail 'published installer is not consumed'
grep -Fq 'HARNESS_LINT_VERSION' "$workflow" || fail 'installer is not pinned to the selected release'
grep -Fq 'HARNESS_LINT_INSTALL_DIR' "$workflow" || fail 'Linux install directory is not isolated'
grep -Fq 'harness-lint hooks status' "$workflow" || fail 'Linux hooks status is not verified'
grep -Fq 'brew install --cask kespineira/tap/harness-lint' "$workflow" || fail 'published Homebrew cask is not installed'
grep -Fq 'HOMEBREW_NO_ANALYTICS' "$workflow" || fail 'Homebrew analytics are not disabled'
grep -Fq 'export HOME=' "$workflow" || fail 'consumer smoke does not isolate HOME'
grep -Fq 'harness-lint version' "$workflow" || fail 'consumer version is not verified'
grep -Fq 'harness-lint --help' "$workflow" || fail 'consumer help is not verified'

if grep -Eiq 'gh[[:space:]]+release|git[[:space:]]+(push|tag|commit)|curl[^\n]*-[[:alnum:]]*[Xx][[:space:]]*(POST|PUT|PATCH|DELETE)|brew[[:space:]]+tap-new' "$workflow"; then
    fail 'workflow contains a repository or release mutation command'
fi

for job in validate-published-release linux-published-consumer macos-published-consumer; do
    grep -Eq "^  $job:$" "$workflow" || fail "$job is missing"
done

echo 'published release smoke workflow policy checks passed'
