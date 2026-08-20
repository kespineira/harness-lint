#!/bin/sh
set -eu

# Consumer E2E for a pre-existing GoReleaser dist directory. This command
# never publishes, downloads, installs globally, or executes a non-host
# package; all five tarballs are still audited by pack-npm-packages.sh.
script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH=; cd -- "$script_dir/.." && pwd)
dist_dir=$project_root/dist
if [ "$#" -gt 0 ]; then
    if [ "$#" -ne 2 ] || [ "$1" != "--dist" ]; then
        echo "npm package E2E: usage: $0 [--dist <GoReleaser dist>]" >&2
        exit 2
    fi
    dist_dir=$2
fi
dist_dir=$(CDPATH=; cd -- "$dist_dir" 2>/dev/null && pwd) || {
    echo "npm package E2E: dist directory does not exist: $dist_dir" >&2
    exit 1
}
require() { command -v "$1" >/dev/null 2>&1 || { echo "npm package E2E: required command is unavailable: $1" >&2; exit 1; }; }
require node
require npm
require python3

tmp_parent=${TMPDIR:-/tmp}
work_dir=$(mktemp -d "$tmp_parent/harness-lint-npm-e2e.XXXXXX")
cleanup() { rm -rf "$work_dir"; }
trap cleanup 0
trap 'cleanup; exit 1' 1 2 3 15
fail() { echo "npm package E2E: $*" >&2; exit 1; }

version=$(python3 - "$dist_dir/metadata.json" <<'PY'
import json
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
try:
    metadata = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"cannot read GoReleaser metadata: {error}")
version = metadata.get("version") if isinstance(metadata, dict) else None
if not isinstance(version, str) or not version:
    raise SystemExit("GoReleaser metadata has no release version")
print(version)
PY
) || fail "could not determine release version"

staging=$work_dir/staging
tarballs=$work_dir/tarballs
"$script_dir/stage-npm-packages.sh" --version "$version" --dist "$dist_dir" --output "$staging" || fail "staging failed"
"$script_dir/pack-npm-packages.sh" --staging "$staging" --output "$tarballs" || fail "npm package packing failed"

root_tarball=$(python3 - "$tarballs/package-receipt.json" <<'PY'
import json
import pathlib
import sys
receipt = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
packages = receipt.get("packages")
if not isinstance(packages, list) or len(packages) != 5:
    raise SystemExit("package receipt does not audit all five tarballs")
for package in packages:
    if not isinstance(package, dict) or not isinstance(package.get("filename"), str):
        raise SystemExit("package receipt has an invalid tarball record")
    if package.get("name") == "harness-lint":
        print(package["filename"])
        break
else:
    raise SystemExit("package receipt has no root tarball")
PY
) || fail "package receipt is invalid"

host_package=$(node - "$tarballs/package-receipt.json" <<'NODE'
const fs = require('node:fs');
const receipt = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const key = `${process.platform}/${process.arch}`;
const names = {
  'darwin/arm64': '@kespineira/harness-lint-darwin-arm64',
  'darwin/x64': '@kespineira/harness-lint-darwin-x64',
  'linux/arm64': '@kespineira/harness-lint-linux-arm64',
  'linux/x64': '@kespineira/harness-lint-linux-x64',
};
const name = names[key];
if (!name) throw new Error(`unsupported host platform: ${key}`);
const packageRecord = receipt.packages.find((entry) => entry.name === name);
if (!packageRecord) throw new Error(`missing host package: ${name}`);
process.stdout.write(packageRecord.filename);
NODE
) || fail "host platform is not supported by the npm package set"

root_path=$tarballs/$root_tarball
native_path=$tarballs/$host_package
[ -f "$root_path" ] || fail "root tarball is missing"
[ -f "$native_path" ] || fail "host native tarball is missing"

# npm's local prefix is intentionally below the temporary workspace. Passing
# the native tarball as a second local package keeps optional dependency
# resolution offline while retaining the root manifest's exact version gate.
prefix=$work_dir/prefix
cache=$work_dir/cache
user_config=$work_dir/npmrc
global_config=$work_dir/global-npmrc
consumer_home=$work_dir/home
consumer_project=$work_dir/project
xdg_config=$work_dir/xdg-config
xdg_data=$work_dir/xdg-data
sqlite_path=$work_dir/state/harness-lint.db
global_sentinel=$work_dir/global-prefix
mkdir -p "$prefix" "$cache" "$consumer_home" "$consumer_project" "$xdg_config" "$xdg_data" "$(dirname "$sqlite_path")" "$global_sentinel"
printf '' > "$user_config"
printf '' > "$global_config"
printf 'sentinel\n' > "$global_sentinel/DO_NOT_TOUCH"
global_before=$(find "$global_sentinel" -mindepth 1 -print | LC_ALL=C sort)
global_guard_before=$(cksum "$global_sentinel/DO_NOT_TOUCH")
export HOME="$consumer_home"
export XDG_CONFIG_HOME="$xdg_config"
export XDG_DATA_HOME="$xdg_data"
export XDG_CACHE_HOME="$cache/xdg"
export NPM_CONFIG_CACHE="$cache"
export NPM_CONFIG_USERCONFIG="$user_config"
export NPM_CONFIG_GLOBALCONFIG="$global_config"
export NPM_CONFIG_PREFIX="$global_sentinel"
export npm_config_cache="$cache"
export npm_config_userconfig="$user_config"
export npm_config_globalconfig="$global_config"
export npm_config_prefix="$global_sentinel"
npm install \
    --prefix "$prefix" \
    --cache "$cache" \
    --userconfig "$user_config" \
    --globalconfig "$global_config" \
    --offline \
    --ignore-scripts \
    --no-audit \
    --no-fund \
    --no-update-notifier \
    --omit=optional \
    "$root_path" "$native_path" >/dev/null || fail "isolated offline npm install failed"

global_after=$(find "$global_sentinel" -mindepth 1 -print | LC_ALL=C sort)
global_guard_after=$(cksum "$global_sentinel/DO_NOT_TOUCH")
[ "$global_before" = "$global_after" ] || fail "npm install modified the redirected global prefix"
[ "$global_guard_before" = "$global_guard_after" ] || fail "npm install modified the global-prefix sentinel"

node - "$prefix" "$version" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const [prefix, version] = process.argv.slice(2);
const link = path.join(prefix, 'node_modules', '.bin', 'harness-lint');
const rootLauncher = path.join(prefix, 'node_modules', 'harness-lint', 'bin', 'harness-lint.js');
const native = path.join(prefix, 'node_modules', '@kespineira',
  `harness-lint-${process.platform}-${process.arch === 'x64' ? 'x64' : process.arch}`, 'bin', 'harness-lint');
if (!fs.existsSync(link) || fs.realpathSync(link) !== fs.realpathSync(rootLauncher)) {
  throw new Error('.bin/harness-lint does not resolve to the root launcher');
}
if (fs.realpathSync(link) === fs.realpathSync(native)) throw new Error('.bin/harness-lint resolves to native binary');
if (!fs.existsSync(native) || !fs.statSync(native).isFile()) throw new Error('host native binary is not installed');
if (JSON.parse(fs.readFileSync(path.join(prefix, 'node_modules', 'harness-lint', 'package.json'))).version !== version) {
  throw new Error('installed root version differs from release version');
}
NODE

launcher=$prefix/node_modules/.bin/harness-lint
native_dir=$prefix/node_modules/@kespineira/harness-lint-$(node -p 'process.platform + "-" + (process.arch === "x64" ? "x64" : process.arch)')
native_version=$("$native_dir/bin/harness-lint" version) || fail "installed native version command failed"
launcher_version=$("$launcher" version) || fail "installed harness-lint version command failed"
[ "$launcher_version" = "$native_version" ] || fail "launcher version output differs from native version output"
case "$launcher_version" in
    "harness-lint version=$version commit="*" build-date="*) ;;
    *) fail "installed harness-lint version output is malformed: $launcher_version" ;;
esac
"$launcher" --help | grep -Fi 'usage:' >/dev/null || fail "installed harness-lint --help output is incomplete"
"$launcher" hooks status >/dev/null || fail "installed harness-lint hooks status command failed"
"$launcher" scan --db "$sqlite_path" --home "$consumer_home" --project "$consumer_project" --now 2026-08-20T12:00:00Z --color never >/dev/null || fail "installed harness-lint scan command failed"
"$launcher" usage --db "$sqlite_path" >/dev/null || fail "installed harness-lint usage command failed"
npm_exec_version=$(npm exec --prefix "$prefix" --cache "$cache" --userconfig "$user_config" --globalconfig "$global_config" --offline --ignore-scripts --no-audit --no-fund --no-update-notifier -- harness-lint version) || fail "npm exec could not run the local harness-lint command"
[ "$npm_exec_version" = "$launcher_version" ] || fail "npm exec version output differs from launcher version output"
global_final=$(find "$global_sentinel" -mindepth 1 -print | LC_ALL=C sort)
global_guard_final=$(cksum "$global_sentinel/DO_NOT_TOUCH")
[ "$global_before" = "$global_final" ] || fail "an installed command modified the redirected global prefix"
[ "$global_guard_before" = "$global_guard_final" ] || fail "an installed command modified the global-prefix sentinel"
echo "npm package E2E passed: audited five packages and executed $version on $(node -p 'process.platform + "/" + process.arch')"
