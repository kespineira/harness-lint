#!/bin/sh
set -eu

# Verify the four canonical archive attestations produced by the stable
# release job. This intentionally uses gh for signature verification and jq
# for the policy that gh's successful exit status cannot express: exact
# repository/workflow/tag identity, subject name, and generated SPDX predicate.
dist=dist
version=
repository=
tag=

fail() {
    echo "release attestation verification: $*" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --dist)
            [ "$#" -ge 2 ] || fail '--dist requires a value'
            dist=$2
            shift 2
            ;;
        --version)
            [ "$#" -ge 2 ] || fail '--version requires a value'
            version=$2
            shift 2
            ;;
        --repo)
            [ "$#" -ge 2 ] || fail '--repo requires a value'
            repository=$2
            shift 2
            ;;
        --tag)
            [ "$#" -ge 2 ] || fail '--tag requires a value'
            tag=$2
            shift 2
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

[ -n "$version" ] || fail '--version is required'
[ -n "$repository" ] || fail '--repo is required'
[ -n "$tag" ] || fail '--tag is required'
[ -d "$dist" ] || fail "dist directory is missing: $dist"
command -v gh >/dev/null 2>&1 || fail 'gh is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'

workflow="$repository/.github/workflows/release.yml"
source_ref="refs/tags/$tag"
provenance_predicate='https://slsa.dev/provenance/v1'
sbom_predicate='https://spdx.dev/Document/v2.3'
verification_dir=$(mktemp -d "${TMPDIR:-/tmp}/harness-lint-attestations.XXXXXX")
cleanup() {
    rm -rf "$verification_dir"
}
trap cleanup EXIT INT TERM

verify_subject_name() {
    result=$1
    archive=$2
    jq -e --arg archive "$archive" '
        length > 0 and all(.[];
            any(.verificationResult.statement.subject[]?; .name == $archive)
        )
    ' "$result" >/dev/null || fail "attestation subject mismatch for $archive"
}

verify_provenance() {
    archive=$1
    result="$verification_dir/$archive.provenance.json"
    # Keep the full archive name and exact release identity in the command so
    # this gate cannot silently verify a different platform or workflow.
    gh attestation verify "$dist/$archive" \
        --repo "$repository" \
        --signer-workflow "$workflow" \
        --source-ref "$source_ref" \
        --predicate-type "$provenance_predicate" \
        --format json >"$result"
    verify_subject_name "$result" "$archive"
}

verify_sbom() {
    archive=$1
    sbom=$2
    result="$verification_dir/$archive.sbom.json"
    jq -e --arg archive "$archive" '.spdxVersion == "SPDX-2.3" and .SPDXID == "SPDXRef-DOCUMENT"' \
        "$dist/$sbom" >/dev/null || fail "generated SBOM is not SPDX 2.3: $sbom"
    gh attestation verify "$dist/$archive" \
        --repo "$repository" \
        --signer-workflow "$workflow" \
        --source-ref "$source_ref" \
        --predicate-type "$sbom_predicate" \
        --format json >"$result"
    jq -e --arg archive "$archive" --slurpfile expected "$dist/$sbom" '
        length > 0 and any(.[];
            any(.verificationResult.statement.subject[]?; .name == $archive) and
            .verificationResult.statement.predicate == $expected[0]
        )
    ' "$result" >/dev/null || fail "SBOM predicate mismatch for $archive"
}

for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
    archive="harness-lint_${version}_${target}.tar.gz"
    sbom="$archive.spdx.json"
    [ -f "$dist/$archive" ] || fail "archive is missing: $archive"
    [ -f "$dist/$sbom" ] || fail "SBOM is missing: $sbom"
    verify_provenance "$archive"
    verify_sbom "$archive" "$sbom"
done

echo 'verified exact build-provenance and SPDX 2.3 attestations for four canonical archives'
