#!/bin/sh
set -eu

# Deterministic, local-only functional test for verify-release-attestations.sh.
# The fake gh command emits attestations from the temporary fixture and logs
# every invocation so this test never contacts GitHub or mutates external state.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
verifier=$script_dir/verify-release-attestations.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/harness-lint-attestation-test.XXXXXX")
cleanup() {
    rm -rf "$temporary"
}
trap cleanup EXIT INT TERM

fail() {
    echo "release attestation functional test: $*" >&2
    exit 1
}

dist=$temporary/dist
fake_bin=$temporary/bin
calls=$temporary/gh-calls
mkdir -p "$dist" "$fake_bin"
: >"$calls"

cat >"$fake_bin/gh" <<'EOF'
#!/bin/sh
set -eu

calls_file=${FAKE_GH_CALLS:?missing FAKE_GH_CALLS}
printf '%s\n' "$*" >>"$calls_file"
[ "$#" -ge 3 ] && [ "$1" = attestation ] && [ "$2" = verify ] || exit 2
artifact=$3
archive=${artifact##*/}
predicate=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --predicate-type)
            predicate=$2
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done

case "$predicate" in
    https://slsa.dev/provenance/v1)
        printf '[{"verificationResult":{"statement":{"subject":[{"name":"%s"}]}}}]\n' "$archive"
        ;;
    https://spdx.dev/Document/v2.3)
        if [ "${FAKE_GH_MODE:-valid}" = wrong-predicate ]; then
            printf '[{"verificationResult":{"statement":{"subject":[{"name":"%s"}],"predicate":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","privateFixtureValue":"do-not-print"}}}}]\n' "$archive"
        else
            jq -c --arg archive "$archive" --slurpfile expected "$artifact.spdx.json" \
                '[{"verificationResult":{"statement":{"subject":[{"name":$archive}],"predicate":$expected[0]}}}]' \
                "$artifact.spdx.json"
        fi
        ;;
    *)
        exit 2
        ;;
esac
EOF
chmod 755 "$fake_bin/gh"

version=1.2.3
repository=example/harness-lint
tag=v$version
workflow=$repository/.github/workflows/release.yml
source_ref=refs/tags/$tag
certificate_identity=https://github.com/$workflow@$source_ref
slsa_predicate=https://slsa.dev/provenance/v1
spdx_predicate=https://spdx.dev/Document/v2.3

for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
    archive=harness-lint_${version}_${target}.tar.gz
    : >"$dist/$archive"
    cat >"$dist/$archive.spdx.json" <<EOF
{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"$archive","documentNamespace":"https://example.invalid/$archive","creationInfo":{"created":"2026-08-18T00:00:00Z","creators":["Tool: syft-1.51.0"]},"packages":[{"SPDXID":"SPDXRef-project","name":"github.com/kespineira/harness-lint"}],"relationships":[{"spdxElementId":"SPDXRef-project","relatedSpdxElement":"SPDXRef-project","relationshipType":"DESCRIBES"}]}
EOF
done

run_verifier() {
    FAKE_GH_CALLS=$calls PATH=$fake_bin:$PATH \
        "$verifier" --dist "$dist" --version "$version" --repo "$repository" --tag "$tag"
}

run_verifier >"$temporary/valid-output"
grep -Fqx 'verified exact build-provenance and SPDX 2.3 attestations for four canonical archives' "$temporary/valid-output" ||
    fail 'valid fixture did not produce the success message'

call_count=$(wc -l <"$calls" | awk '{ print $1 }')
[ "$call_count" -eq 8 ] || fail "expected eight gh calls, got $call_count"
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
    archive=harness-lint_${version}_${target}.tar.gz
    provenance_line="attestation verify $dist/$archive --repo $repository --signer-workflow $workflow --source-ref $source_ref --cert-identity $certificate_identity --predicate-type $slsa_predicate --format json"
    sbom_line="attestation verify $dist/$archive --repo $repository --signer-workflow $workflow --source-ref $source_ref --cert-identity $certificate_identity --predicate-type $spdx_predicate --format json"
    for expected in "$provenance_line" "$sbom_line"; do
        matches=$(awk -v expected="$expected" '$0 == expected { count++ } END { print count + 0 }' "$calls")
        [ "$matches" -eq 1 ] || fail "expected exactly one gh call: $expected"
    done
done

: >"$calls"
if FAKE_GH_MODE=wrong-predicate FAKE_GH_CALLS=$calls PATH=$fake_bin:$PATH \
    "$verifier" --dist "$dist" --version "$version" --repo "$repository" --tag "$tag" \
    >"$temporary/failure-output" 2>"$temporary/failure-error"; then
    fail 'wrong SBOM predicate was accepted'
fi
grep -Fq 'SBOM predicate mismatch for harness-lint_1.2.3_darwin_amd64.tar.gz' "$temporary/failure-error" ||
    fail 'wrong SBOM predicate did not produce the expected safe failure'
if grep -Fq 'do-not-print' "$temporary/failure-output" "$temporary/failure-error"; then
    fail 'predicate failure exposed fixture-private data'
fi

echo 'release attestation functional checks passed'
