#!/bin/sh
set -eu

# Consumer-facing release gate: build GoReleaser's snapshot and inspect exactly
# what a release consumer downloads. Only the matching native artifact runs.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH=; cd -- "$script_dir/.." && pwd)
cd "$project_root"
tmp_parent=${TMPDIR:-/tmp}
work_dir=$(mktemp -d "$tmp_parent/harness-lint-release-e2e.XXXXXX")
cleanup() { rm -rf "$work_dir"; }
trap cleanup 0
trap 'cleanup; exit 1' 1 2 3 15
fail() { echo "release E2E: $*" >&2; exit 1; }
require() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }
file_description_matches() {
    file_os=$1
    file_arch=$2
    file_description=$3
    case "$file_os/$file_arch" in
        darwin/amd64)
            case "$file_description" in
                'Mach-O 64-bit executable x86_64'|\
                'Mach-O 64-bit executable x86_64, flags:'*|\
                'Mach-O 64-bit x86_64 executable, flags:'*) return 0 ;;
            esac
            ;;
        darwin/arm64)
            case "$file_description" in
                'Mach-O 64-bit executable arm64'|\
                'Mach-O 64-bit executable arm64, flags:'*|\
                'Mach-O 64-bit arm64 executable, flags:'*) return 0 ;;
            esac
            ;;
        linux/amd64)
            case "$file_description" in
                'ELF 64-bit LSB executable, x86-64,'*) return 0 ;;
            esac
            ;;
        linux/arm64)
            case "$file_description" in
                'ELF 64-bit LSB executable, ARM aarch64,'*) return 0 ;;
            esac
            ;;
    esac
    return 1
}
run_file_description_tests() {
    file_description_matches darwin amd64 \
        'Mach-O 64-bit executable x86_64' || fail 'macOS amd64 file description was rejected'
    file_description_matches darwin arm64 \
        'Mach-O 64-bit executable arm64' || fail 'macOS arm64 file description was rejected'
    file_description_matches darwin amd64 \
        'Mach-O 64-bit x86_64 executable, flags:<|DYLDLINK|PIE>' ||
        fail 'Ubuntu amd64 file description was rejected'
    file_description_matches darwin arm64 \
        'Mach-O 64-bit arm64 executable, flags:<|DYLDLINK|PIE>' ||
        fail 'Ubuntu arm64 file description was rejected'
    file_description_matches linux amd64 \
        'ELF 64-bit LSB executable, x86-64, version 1 (SYSV)' ||
        fail 'Linux amd64 file description was rejected'
    file_description_matches linux arm64 \
        'ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV)' ||
        fail 'Linux arm64 file description was rejected'
    if file_description_matches darwin amd64 \
        'ELF 64-bit LSB executable, x86-64, version 1 (SYSV)'; then
        fail 'Darwin amd64 matcher accepted a Linux binary'
    fi
    if file_description_matches darwin amd64 \
        'Mach-O 64-bit arm64 executable, flags:<|DYLDLINK|PIE>'; then
        fail 'Darwin amd64 matcher accepted an arm64 binary'
    fi
    if file_description_matches linux amd64 \
        'ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV)'; then
        fail 'Linux amd64 matcher accepted an arm64 binary'
    fi
    echo 'release E2E file description tests passed'
}
if [ "${1:-}" = --test-file-descriptions ]; then
    run_file_description_tests
    exit 0
fi
require goreleaser
require tar
require file
require ruby
require curl
require awk
require sort
require cmp
require python3
if command -v sha256sum >/dev/null 2>&1; then checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum_tool=shasum
else fail 'required SHA-256 command is unavailable'
fi
checksum_for() {
    case "$checksum_tool" in
        sha256sum) sha256sum "$1" | awk '{print $1}' ;;
        shasum) shasum -a 256 "$1" | awk '{print $1}' ;;
    esac
}

goreleaser release --snapshot --clean --skip=publish
dist_dir=$project_root/dist
[ -d "$dist_dir" ] || fail 'GoReleaser did not create dist'
version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$dist_dir/metadata.json" | sed -n '1p')
[ -n "$version" ] || fail 'GoReleaser metadata has no version'

# Validate the exact stable output that GoReleaser just created. This gate is
# intentionally local and read-only: it does not rebuild or execute artifacts.
PYTHONDONTWRITEBYTECODE=1 "$script_dir/validate-release-dist.sh" \
    --dist "$dist_dir" --version "$version"

for os in darwin linux; do
    for arch in amd64 arm64; do
        archive=$dist_dir/harness-lint_${version}_${os}_${arch}.tar.gz
        tar -tzf "$archive" > "$work_dir/entries" || fail "cannot inspect $archive"
        printf '%s\n' LICENSE README.md harness-lint | sort > "$work_dir/want-entries"
        sed 's#^\./##' "$work_dir/entries" | sort > "$work_dir/got-entries"
        cmp -s "$work_dir/want-entries" "$work_dir/got-entries" || fail "unexpected archive structure in $(basename "$archive")"
        extract=$work_dir/extract-$os-$arch
        mkdir -p "$extract"
        # Checking extracted entries with POSIX test avoids depending on the
        # implementation-specific mode column emitted by tar -tv.
        tar -xzf "$archive" -C "$extract" || fail "cannot extract archive"
        for member in harness-lint LICENSE README.md; do
            [ -f "$extract/$member" ] || fail "archive member is not a regular file: $member"
            [ ! -L "$extract/$member" ] || fail "archive member is a link: $member"
        done
        [ -x "$extract/harness-lint" ] || fail "archive binary is not executable"
        [ ! -x "$extract/LICENSE" ] && [ ! -x "$extract/README.md" ] ||
            fail "archive documentation has executable mode"
        description=$(file -b "$extract/harness-lint")
        file_description_matches "$os" "$arch" "$description" ||
            fail "wrong $os/$arch architecture: $description"
        echo "validated archive $(basename "$archive"): $description (not executed)"
    done
done

if command -v dpkg-deb >/dev/null 2>&1; then
    deb_version=$(printf '%s' "$version" | sed 's/-/~/')
    for arch in amd64 arm64; do
        package=$dist_dir/harness-lint_${version}_linux_${arch}.deb
        [ "$(dpkg-deb --field "$package" Package)" = harness-lint ] || fail 'Debian package name mismatch'
        [ "$(dpkg-deb --field "$package" Version)" = "$deb_version" ] || fail 'Debian package version mismatch'
        [ "$(dpkg-deb --field "$package" Architecture)" = "$arch" ] || fail 'Debian package architecture mismatch'
        [ "$(dpkg-deb --field "$package" Homepage)" = https://github.com/kespineira/harness-lint ] || fail 'Debian package Homepage mismatch'
        dpkg-deb --contents "$package" | awk '$NF == "./usr/bin/harness-lint" { found=1 } END { exit !found }' || fail 'Debian package misses /usr/bin/harness-lint'
        dpkg-deb --contents "$package" | awk '$NF == "./usr/share/doc/harness-lint/LICENSE" { found=1 } END { exit !found }' || fail 'Debian package misses LICENSE payload'
        extract=$work_dir/deb-$arch
        mkdir -p "$extract"
        dpkg-deb --extract "$package" "$extract" >/dev/null
        [ -x "$extract/usr/bin/harness-lint" ] || fail 'Debian payload is not executable'
        cmp -s LICENSE "$extract/usr/share/doc/harness-lint/LICENSE" || fail 'Debian LICENSE payload differs from repository LICENSE'
        echo "validated Debian metadata and payload $(basename "$package") (not executed)"
    done
else
    echo 'dpkg-deb unavailable; Debian metadata/payload inspection not run' >&2
fi

if command -v rpm >/dev/null 2>&1; then
    rpm_version=$version
    rpm_release=1
    case "$version" in
        *-*)
            # nFPM maps the first prerelease separator to '~' and subsequent
            # separators to '_'; RPM release remains the package release 1.
            rpm_version=${version%%-*}~${version#*-}
            rpm_version=$(printf '%s' "$rpm_version" | tr '-' '_')
            ;;
    esac
    for arch in amd64 arm64; do
        package=$dist_dir/harness-lint_${version}_linux_${arch}.rpm
        [ "$(rpm -qp --qf '%{NAME}' "$package")" = harness-lint ] || fail 'RPM package name mismatch'
        [ "$(rpm -qp --qf '%{VERSION}' "$package")" = "$rpm_version" ] || fail 'RPM package version mismatch'
        [ "$(rpm -qp --qf '%{RELEASE}' "$package")" = "$rpm_release" ] || fail 'RPM package release mismatch'
        [ "$(rpm -qp --qf '%{LICENSE}' "$package")" = Apache-2.0 ] || fail 'RPM license metadata mismatch'
        rpm_arch=$(rpm -qp --qf '%{ARCH}' "$package")
        case "$arch/$rpm_arch" in amd64/x86_64|arm64/aarch64|arm64/arm64) ;; *) fail 'RPM architecture mismatch' ;; esac
        rpm -qpl "$package" | grep -Fx '/usr/bin/harness-lint' >/dev/null || fail 'RPM misses /usr/bin/harness-lint'
        rpm -qpl "$package" | grep -Fx '/usr/share/doc/harness-lint/LICENSE' >/dev/null || fail 'RPM misses LICENSE payload'
        echo "validated RPM metadata and payload $(basename "$package") (not executed)"
    done
else
    echo 'rpm unavailable; RPM metadata/payload inspection not run' >&2
fi

if command -v apk >/dev/null 2>&1; then
    for arch in amd64 arm64; do
        package=$dist_dir/harness-lint_${version}_linux_${arch}.apk
        apk manifest "$package" > "$work_dir/apk-$arch.manifest" || fail 'apk could not inspect package'
        [ -s "$work_dir/apk-$arch.manifest" ] || fail 'apk manifest is empty'
    done
fi
for arch in amd64 arm64; do
    package=$dist_dir/harness-lint_${version}_linux_${arch}.apk
    apk_version=$version
    case "$version" in *-*) apk_version=${version%%-*}_${version#*-} ;; esac
    gzip -dc "$package" | tar -xOf - .PKGINFO > "$work_dir/apk-$arch.pkginfo" || fail 'APK has no .PKGINFO'
    grep -Fx 'pkgname = harness-lint' "$work_dir/apk-$arch.pkginfo" >/dev/null || fail 'APK package name mismatch'
    grep -Fx "pkgver = $apk_version" "$work_dir/apk-$arch.pkginfo" >/dev/null || fail 'APK package version mismatch'
    grep -Fx 'license = Apache-2.0' "$work_dir/apk-$arch.pkginfo" >/dev/null || fail 'APK license metadata mismatch'
    case "$arch" in amd64) apk_arch=x86_64 ;; arm64) apk_arch=aarch64 ;; esac
    grep -Fx "arch = $apk_arch" "$work_dir/apk-$arch.pkginfo" >/dev/null || fail 'APK architecture mismatch'
    extract=$work_dir/apk-$arch
    mkdir -p "$extract"
    gzip -dc "$package" | tar -xf - -C "$extract"
    [ -x "$extract/usr/bin/harness-lint" ] || fail 'APK payload is not executable'
    [ -f "$extract/usr/share/doc/harness-lint/LICENSE" ] || fail 'APK misses LICENSE payload'
    echo "validated APK metadata and payload $(basename "$package") (not executed)"
done

cask=$dist_dir/homebrew/Casks/harness-lint.rb
[ -r "$cask" ] || fail 'GoReleaser did not generate a Homebrew Cask'
# GoReleaser v2.17.1 has no template/configuration hook for Homebrew's current
# architecture stanza order. Normalize only the generated structural layout;
# version, URLs, and checksums remain GoReleaser-owned.
PYTHONDONTWRITEBYTECODE=1 python3 "$script_dir/normalize_homebrew_cask.py" "$cask"
ruby -c "$cask" >/dev/null || fail 'generated Homebrew Cask is not valid Ruby'
if grep -Eq '^[[:space:]]+license[[:space:]]' "$cask"; then fail 'generated Cask has an unsupported top-level license stanza'; fi
if grep -Eq '^  desc "[^\"]*\."$' "$cask"; then fail 'generated Cask description ends with a full stop'; fi
if grep -Fq 'com.apple.quarantine' "$cask"; then fail 'generated Cask contains an unsigned-binary quarantine hook'; fi
if ! awk '/^    on_arm do$/ { arm = NR } /^    on_intel do$/ { intel = NR } END { exit !(arm && intel && arm < intel) }' "$cask"; then
    fail 'generated Cask does not place on_arm before on_intel'
fi
if ! awk 'previous == "  end" && $0 == "  on_linux do" { found = 1 } { previous = $0 } END { exit !found }' "$cask"; then
    fail 'generated Cask has a blank separator between OS blocks'
fi
if ! awk 'END { if (NR < 2 || $0 != "end" || previous == "") exit 1 } { previous = $0 }' "$cask"; then
    fail 'generated Cask has an empty line before its closing end'
fi
echo 'validated and normalized generated Homebrew Cask (brew style/audit/load run only in macOS CI)'

host_os=$(uname -s)
host_arch=$(uname -m)
case "$host_os" in Darwin) host_os=darwin ;; Linux) host_os=linux ;; *) host_os=unsupported ;; esac
case "$host_arch" in x86_64|amd64) host_arch=amd64 ;; arm64|aarch64) host_arch=arm64 ;; *) host_arch=unsupported ;; esac
if [ "$host_os" != unsupported ] && [ "$host_arch" != unsupported ]; then
    fixture=$work_dir/release-fixture
    mkdir -p "$fixture/v$version"
    target=harness-lint_${version}_${host_os}_${host_arch}.tar.gz
    cp "$dist_dir/$target" "$fixture/v$version/$target"
    cp "$dist_dir/checksums.txt" "$fixture/v$version/checksums.txt"
    install_dir=$work_dir/install/bin
    mkdir -p "$install_dir"
    printf '%s\n' old-release-binary > "$install_dir/harness-lint"
    chmod 755 "$install_dir/harness-lint"
    HARNESS_LINT_UNAME_S=$(uname -s) HARNESS_LINT_UNAME_M=$(uname -m) \
        HARNESS_LINT_VERSION="$version" HARNESS_LINT_RELEASE_BASE_URL="file://$fixture" \
        HARNESS_LINT_INSTALL_DIR="$install_dir" PATH=/usr/bin:/bin \
        "$script_dir/install.sh" > "$work_dir/install.out" 2> "$work_dir/install.err"
    installed_version=$("$install_dir/harness-lint" version)
    case "$installed_version" in "harness-lint version=$version commit="*) ;; *) fail "unexpected installed metadata: $installed_version" ;; esac
    echo "executed matching native binary $target: $installed_version"
    bad_version=$(printf '%s' "$version-checksum-failure")
    bad_archive=harness-lint_${bad_version}_${host_os}_${host_arch}.tar.gz
    mkdir -p "$fixture/v$bad_version"
    cp "$dist_dir/$target" "$fixture/v$bad_version/$bad_archive"
    printf '%064d  %s\n' 0 "$bad_archive" > "$fixture/v$bad_version/checksums.txt"
    before_bad_checksum=$(checksum_for "$install_dir/harness-lint")
    if HARNESS_LINT_UNAME_S=$(uname -s) HARNESS_LINT_UNAME_M=$(uname -m) \
        HARNESS_LINT_VERSION="$bad_version" HARNESS_LINT_RELEASE_BASE_URL="file://$fixture" \
        HARNESS_LINT_INSTALL_DIR="$install_dir" PATH=/usr/bin:/bin \
        "$script_dir/install.sh" > "$work_dir/install-bad.out" 2> "$work_dir/install-bad.err"; then
        fail 'installer accepted a checksum failure'
    fi
    grep -F 'SHA-256 checksum verification failed' "$work_dir/install-bad.err" >/dev/null || fail 'installer checksum failure message is missing'
    [ -x "$install_dir/harness-lint" ] || fail 'checksum failure removed existing install'
    after_bad_checksum=$(checksum_for "$install_dir/harness-lint")
    [ "$before_bad_checksum" = "$after_bad_checksum" ] || fail 'checksum failure changed existing install'
    echo 'validated installer checksum failure preserves existing binary and checksum'
else
    echo "installer execution skipped on unsupported runner $host_os/$host_arch"
fi
echo "release E2E passed for snapshot $version"
