#!/bin/sh
set -eu

test_script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
test_installer=$test_script_dir/install.sh
test_root=$(mktemp -d "${TMPDIR:-/tmp}/harness-lint-install-tests.XXXXXX")
test_cleanup() {
    rm -rf "$test_root"
}
trap test_cleanup 0
trap 'test_cleanup; exit 1' 1 2 3 15

test_tmp=$test_root/tmp
test_fixture=$test_root/fixture
test_api=$test_fixture/api
test_releases=$test_fixture/releases
test_source=$test_fixture/source
test_home=$test_root/home
mkdir -p "$test_tmp" "$test_api/repos/example/project/releases" "$test_releases" "$test_source" "$test_home"

test_fail() {
    echo "install test: $*" >&2
    exit 1
}

test_assert_file_contains() {
    test_file=$1
    test_text=$2
    grep -F "$test_text" "$test_file" >/dev/null 2>&1 || test_fail "missing '$test_text' in $test_file"
}

test_assert_no_temp_dirs() {
    test_leftover=$(find "$test_tmp" -mindepth 1 -maxdepth 1 -name 'harness-lint-install.*' -print -quit)
    [ -z "$test_leftover" ] || test_fail "installer temporary directory was not cleaned: $test_leftover"
}

test_make_release() {
    test_version=$1
    test_os=$2
    test_arch=$3
    test_marker=$4
    test_release_dir=$test_releases/v$test_version
    test_artifact=harness-lint_${test_version}_${test_os}_${test_arch}.tar.gz
    mkdir -p "$test_release_dir"
    printf '%s\n' "$test_marker" > "$test_source/harness-lint"
    chmod 755 "$test_source/harness-lint"
    tar -czf "$test_release_dir/$test_artifact" -C "$test_source" harness-lint
    if command -v sha256sum >/dev/null 2>&1; then
        test_digest=$(sha256sum "$test_release_dir/$test_artifact" | awk '{print $1}')
    else
        test_digest=$(shasum -a 256 "$test_release_dir/$test_artifact" | awk '{print $1}')
    fi
    printf '%s  %s\n' "$test_digest" "$test_artifact" >> "$test_release_dir/checksums.txt"
}

test_run_install() {
    test_output=$1
    test_errors=$2
    shift 2
    if ! env -i \
        HOME="$test_home" \
        PATH="${test_path:-/usr/bin:/bin}" \
        TMPDIR="$test_tmp" \
        HARNESS_LINT_REPOSITORY=example/project \
        HARNESS_LINT_API_URL="file://$test_api" \
        HARNESS_LINT_RELEASE_BASE_URL="file://$test_releases" \
        "$@" \
        "$test_installer" >"$test_output" 2>"$test_errors"; then
        return 1
    fi
}

printf '{"tag_name":"v1.2.3"}\n' > "$test_api/repos/example/project/releases/latest"
test_make_release 1.2.3 darwin amd64 mapping-darwin-amd64
test_make_release 1.2.3 darwin arm64 mapping-darwin-arm64
test_make_release 1.2.3 linux amd64 mapping-linux-amd64
test_make_release 1.2.3 linux arm64 mapping-linux-arm64
test_make_release 2.0.0 linux amd64 pinned-version

# The four supported OS/architecture mappings are selected without uname calls.
test_mapping_index=0
for test_platform in "Darwin x86_64 darwin amd64" "Darwin arm64 darwin arm64" "Linux x86_64 linux amd64" "Linux aarch64 linux arm64"; do
    # The fixture tuples contain only literal, space-separated words.
    # shellcheck disable=SC2086
    set -- $test_platform
    test_mapping_index=$((test_mapping_index + 1))
    test_destination=$test_root/mappings/$test_mapping_index/bin
    test_output=$test_root/mapping-$test_mapping_index.out
    test_errors=$test_root/mapping-$test_mapping_index.err
    test_path=/usr/bin:/bin
    test_run_install "$test_output" "$test_errors" \
        HARNESS_LINT_UNAME_S="$1" HARNESS_LINT_UNAME_M="$2" \
        HARNESS_LINT_VERSION=v1.2.3 HARNESS_LINT_INSTALL_DIR="$test_destination" || test_fail "mapping $test_platform failed"
    test_assert_file_contains "$test_destination/harness-lint" "mapping-$3-$4"
    test_assert_file_contains "$test_errors" "Add $test_destination to PATH"
    test_assert_file_contains "$test_errors" "Cosign is not installed"
    test_assert_no_temp_dirs
done

# Latest resolution uses the local API fixture; a pinned version bypasses it.
test_latest_destination=$test_root/latest/bin
test_path=$test_latest_destination:/usr/bin:/bin
test_run_install "$test_root/latest.out" "$test_root/latest.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=amd64 \
    HARNESS_LINT_INSTALL_DIR="$test_latest_destination" || test_fail "latest release install failed"
test_assert_file_contains "$test_latest_destination/harness-lint" "mapping-linux-amd64"
test_assert_no_temp_dirs

# With no install override the installer uses the disposable HOME, never sudo.
test_default_destination=$test_home/.local/bin
test_path=/usr/bin:/bin
test_run_install "$test_root/default.out" "$test_root/default.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=amd64 \
    HARNESS_LINT_VERSION=1.2.3 || test_fail "default install directory failed"
test_assert_file_contains "$test_default_destination/harness-lint" "mapping-linux-amd64"
test_assert_file_contains "$test_root/default.err" "Add $test_default_destination to PATH"
test_assert_file_contains "$test_root/default.out" "Installed harness-lint 1.2.3"
test_assert_no_temp_dirs

test_pinned_destination=$test_root/pinned/bin
test_path=/usr/bin:/bin
test_run_install "$test_root/pinned.out" "$test_root/pinned.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=x86_64 \
    HARNESS_LINT_VERSION=2.0.0 HARNESS_LINT_INSTALL_DIR="$test_pinned_destination" || test_fail "pinned release install failed"
test_assert_file_contains "$test_pinned_destination/harness-lint" "pinned-version"
test_assert_no_temp_dirs

# An unsupported platform is rejected before any endpoint is contacted or directory is created.
test_unsupported_destination=$test_root/unsupported/bin
if test_run_install "$test_root/unsupported.out" "$test_root/unsupported.err" \
    HARNESS_LINT_UNAME_S=FreeBSD HARNESS_LINT_UNAME_M=amd64 \
    HARNESS_LINT_VERSION=1.2.3 HARNESS_LINT_INSTALL_DIR="$test_unsupported_destination"; then
    test_fail "unsupported operating system was accepted"
fi
test_assert_file_contains "$test_root/unsupported.err" "unsupported operating system: FreeBSD"
[ ! -e "$test_unsupported_destination" ] || test_fail "unsupported OS created install directory"
test_assert_no_temp_dirs

test_unsupported_destination=$test_root/unsupported-arch/bin
if test_run_install "$test_root/unsupported-arch.out" "$test_root/unsupported-arch.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=ppc64 \
    HARNESS_LINT_VERSION=1.2.3 HARNESS_LINT_INSTALL_DIR="$test_unsupported_destination"; then
    test_fail "unsupported architecture was accepted"
fi
test_assert_file_contains "$test_root/unsupported-arch.err" "unsupported architecture: ppc64"
[ ! -e "$test_unsupported_destination" ] || test_fail "unsupported architecture created install directory"
test_assert_no_temp_dirs

# A bad checksum cannot replace an existing binary and leaves no install directory artifacts.
test_bad_version=9.9.9
test_bad_dir=$test_releases/v$test_bad_version
mkdir -p "$test_bad_dir"
test_bad_artifact=harness-lint_${test_bad_version}_linux_amd64.tar.gz
printf 'replacement-must-not-land\n' > "$test_source/harness-lint"
chmod 755 "$test_source/harness-lint"
tar -czf "$test_bad_dir/$test_bad_artifact" -C "$test_source" harness-lint
printf '%064d  %s\n' 0 "$test_bad_artifact" > "$test_bad_dir/checksums.txt"
test_existing_destination=$test_root/existing/bin
mkdir -p "$test_existing_destination"
printf 'old-binary\n' > "$test_existing_destination/harness-lint"
chmod 755 "$test_existing_destination/harness-lint"
if test_run_install "$test_root/bad.out" "$test_root/bad.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=amd64 \
    HARNESS_LINT_VERSION="$test_bad_version" HARNESS_LINT_INSTALL_DIR="$test_existing_destination"; then
    test_fail "bad checksum was accepted"
fi
test_assert_file_contains "$test_root/bad.err" "SHA-256 checksum verification failed"
test_assert_file_contains "$test_existing_destination/harness-lint" "old-binary"
test_assert_no_temp_dirs

# Existing binaries are upgraded atomically after successful verification.
test_upgrade_source=$test_root/upgrade/bin
test_path=$test_upgrade_source:/usr/bin:/bin
test_run_install "$test_root/upgrade.out" "$test_root/upgrade.err" \
    HARNESS_LINT_UNAME_S=Linux HARNESS_LINT_UNAME_M=amd64 \
    HARNESS_LINT_VERSION=2.0.0 HARNESS_LINT_INSTALL_DIR="$test_upgrade_source" || test_fail "upgrade install failed"
test_assert_file_contains "$test_upgrade_source/harness-lint" "pinned-version"
test_assert_no_temp_dirs

echo "installer tests passed"
