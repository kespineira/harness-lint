#!/bin/sh
set -eu

# Test-only overrides are intentionally narrow and are useful for deterministic
# local fixtures: HARNESS_LINT_UNAME_S, HARNESS_LINT_UNAME_M,
# HARNESS_LINT_REPOSITORY, HARNESS_LINT_API_URL, and
# HARNESS_LINT_RELEASE_BASE_URL.  In normal use the defaults below point at
# the canonical GitHub repository and release endpoints.

installer_die() {
    echo "harness-lint installer: $*" >&2
    exit 1
}

installer_require_url() {
    case "$1" in
        https://*|file://*|http://localhost*|http://127.0.0.1*|http://\[::1\]*) ;;
        *) installer_die "refusing unsafe endpoint: $1" ;;
    esac
}

installer_validate_version() {
    case "$1" in
        ''|*[!A-Za-z0-9._+-]*|.*|*-|*.)
            installer_die "invalid release version: $1"
            ;;
    esac
}

installer_validate_repository() {
    installer_repository=$1
    case "$installer_repository" in
        */*)
            installer_owner=${installer_repository%%/*}
            installer_name=${installer_repository#*/}
            ;;
        *) installer_die "invalid GitHub repository: $installer_repository" ;;
    esac
    case "$installer_owner" in
        ''|*[!A-Za-z0-9._-]*) installer_die "invalid GitHub repository: $installer_repository" ;;
    esac
    case "$installer_name" in
        ''|*/*|*[!A-Za-z0-9._-]*) installer_die "invalid GitHub repository: $installer_repository" ;;
    esac
}

installer_cleanup() {
    if [ -n "${installer_stage-}" ]; then
        rm -f "$installer_stage"
    fi
    if [ -n "${installer_tmp-}" ]; then
        rm -rf "$installer_tmp"
    fi
}

installer_tmp_parent=${TMPDIR:-/tmp}
installer_tmp=$(mktemp -d "$installer_tmp_parent/harness-lint-install.XXXXXX") || installer_die "unable to create temporary directory"
trap installer_cleanup 0
trap 'installer_cleanup; exit 1' 1 2 3 15

if [ "${HARNESS_LINT_UNAME_S+x}" = x ]; then
    installer_uname_s=$HARNESS_LINT_UNAME_S
else
    installer_uname_s=$(uname -s)
fi
if [ "${HARNESS_LINT_UNAME_M+x}" = x ]; then
    installer_uname_m=$HARNESS_LINT_UNAME_M
else
    installer_uname_m=$(uname -m)
fi

case "$installer_uname_s" in
    Darwin) installer_os=darwin ;;
    Linux) installer_os=linux ;;
    *) installer_die "unsupported operating system: $installer_uname_s" ;;
esac
case "$installer_uname_m" in
    x86_64|amd64) installer_arch=amd64 ;;
    arm64|aarch64) installer_arch=arm64 ;;
    *) installer_die "unsupported architecture: $installer_uname_m" ;;
esac

installer_repository=${HARNESS_LINT_REPOSITORY:-kespineira/harness-lint}
installer_validate_repository "$installer_repository"

installer_version_input=${HARNESS_LINT_VERSION:-}
if [ -n "$installer_version_input" ]; then
    case "$installer_version_input" in
        v*) installer_version=${installer_version_input#v} ;;
        *) installer_version=$installer_version_input ;;
    esac
    installer_validate_version "$installer_version"
else
    installer_api_url=${HARNESS_LINT_API_URL:-https://api.github.com}
    installer_require_url "$installer_api_url"
    installer_metadata="$installer_tmp/latest.json"
    if ! curl -fsSL -A harness-lint-installer \
        "${installer_api_url%/}/repos/$installer_repository/releases/latest" \
        -o "$installer_metadata"; then
        installer_die "unable to resolve the latest GitHub release"
    fi
    installer_tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$installer_metadata" | sed -n '1p')
    case "$installer_tag" in
        v*) installer_version=${installer_tag#v} ;;
        *) installer_die "latest release response has no v-prefixed tag" ;;
    esac
    installer_validate_version "$installer_version"
fi

installer_artifact="harness-lint_${installer_version}_${installer_os}_${installer_arch}.tar.gz"
installer_release_base=${HARNESS_LINT_RELEASE_BASE_URL:-https://github.com/$installer_repository/releases/download}
installer_require_url "$installer_release_base"
installer_artifact_url="${installer_release_base%/}/v${installer_version}/${installer_artifact}"
installer_checksums_url="${installer_release_base%/}/v${installer_version}/checksums.txt"
installer_bundle_url="${installer_release_base%/}/v${installer_version}/checksums.txt.sigstore.json"

installer_archive="$installer_tmp/$installer_artifact"
installer_checksums="$installer_tmp/checksums.txt"
if ! curl -fsSL -A harness-lint-installer "$installer_artifact_url" -o "$installer_archive"; then
    installer_die "unable to download $installer_artifact"
fi
if ! curl -fsSL -A harness-lint-installer "$installer_checksums_url" -o "$installer_checksums"; then
    installer_die "unable to download checksums.txt"
fi

if command -v sha256sum >/dev/null 2>&1; then
    installer_checksum_command=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    installer_checksum_command=shasum
else
    installer_die "neither sha256sum nor shasum is available"
fi

installer_expected_checksum=$(awk -v target="$installer_artifact" '
    BEGIN { count = 0; value = "" }
    NF == 2 {
        name = $2
        sub(/^\*/, "", name)
        if (name == target) {
            count++
            value = $1
        }
    }
    END {
        if (count != 1 || length(value) != 64 || value !~ /^[0-9A-Fa-f]+$/) exit 1
        print value
    }
' "$installer_checksums") || installer_die "checksums.txt has no unique SHA-256 entry for $installer_artifact"

if [ "$installer_checksum_command" = sha256sum ]; then
    installer_actual_checksum=$(sha256sum "$installer_archive" | awk '{print $1}')
else
    installer_actual_checksum=$(shasum -a 256 "$installer_archive" | awk '{print $1}')
fi
installer_expected_checksum=$(printf '%s' "$installer_expected_checksum" | tr '[:upper:]' '[:lower:]')
installer_actual_checksum=$(printf '%s' "$installer_actual_checksum" | tr '[:upper:]' '[:lower:]')
if [ "$installer_expected_checksum" != "$installer_actual_checksum" ]; then
    installer_die "SHA-256 checksum verification failed for $installer_artifact"
fi

if command -v cosign >/dev/null 2>&1; then
    installer_bundle="$installer_tmp/checksums.txt.sigstore.json"
    if curl -fsSL -A harness-lint-installer "$installer_bundle_url" -o "$installer_bundle" 2>/dev/null; then
        installer_identity="https://$installer_repository/.github/workflows/release.yml@refs/tags/v${installer_version}"
        if ! cosign verify-blob "$installer_checksums" \
            --bundle "$installer_bundle" \
            --certificate-identity "$installer_identity" \
            --certificate-oidc-issuer https://token.actions.githubusercontent.com; then
            installer_die "Cosign checksum authenticity verification failed"
        fi
        echo "Checksum authenticity verified with Cosign." >&2
    else
        echo "Cosign is installed, but this release has no checksum signature bundle; SHA-256 was verified." >&2
    fi
else
    echo "Cosign is not installed; SHA-256 was verified, but checksum authenticity was not." >&2
fi

installer_extract="$installer_tmp/extract"
mkdir -p "$installer_extract"
installer_listing="$installer_tmp/archive.list"
if ! tar -tzf "$installer_archive" > "$installer_listing"; then
    installer_die "unable to inspect release archive"
fi
while IFS= read -r installer_entry; do
    case "$installer_entry" in
        ''|/*|../*|*/../*|*/..|.|./*) installer_die "release archive contains an unsafe path" ;;
    esac
done < "$installer_listing"
installer_verbose_listing="$installer_tmp/archive.verbose.list"
if ! tar -tvzf "$installer_archive" > "$installer_verbose_listing"; then
    installer_die "unable to inspect release archive entries"
fi
while IFS= read -r installer_verbose_entry; do
    case "$installer_verbose_entry" in
        l*|h*) installer_die "release archive contains a link entry" ;;
    esac
done < "$installer_verbose_listing"
if ! tar -xzf "$installer_archive" -C "$installer_extract"; then
    installer_die "unable to unpack release archive"
fi
installer_binary="$installer_extract/harness-lint"
if [ ! -f "$installer_binary" ] || [ -L "$installer_binary" ]; then
    installer_die "release archive does not contain a regular harness-lint binary"
fi

if [ "${HARNESS_LINT_INSTALL_DIR+x}" = x ]; then
    installer_install_dir=$HARNESS_LINT_INSTALL_DIR
else
    installer_home=${HOME:-}
    [ -n "$installer_home" ] || installer_die "HOME is not set; use HARNESS_LINT_INSTALL_DIR"
    installer_install_dir=$installer_home/.local/bin
fi
[ -n "$installer_install_dir" ] || installer_die "install directory is empty"
mkdir -p "$installer_install_dir" || installer_die "unable to create install directory: $installer_install_dir"

installer_stage=$(mktemp "$installer_install_dir/.harness-lint.XXXXXX") || installer_die "unable to stage binary in install directory"
if ! cp "$installer_binary" "$installer_stage"; then
    installer_die "unable to stage binary in install directory"
fi
chmod 755 "$installer_stage" || installer_die "unable to make staged binary executable"
if ! mv -f "$installer_stage" "$installer_install_dir/harness-lint"; then
    installer_die "unable to atomically install harness-lint"
fi
installer_stage=

echo "Installed harness-lint $installer_version ($installer_os/$installer_arch) to $installer_install_dir/harness-lint"
case ":${PATH:-}:" in
    *:"$installer_install_dir":*) ;;
    *) echo "Add $installer_install_dir to PATH to run harness-lint." >&2 ;;
esac
