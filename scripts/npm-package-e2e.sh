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
mkdir -p "$prefix" "$cache"
printf '' > "$user_config"
npm install \
    --prefix "$prefix" \
    --cache "$cache" \
    --userconfig "$user_config" \
    --offline \
    --ignore-scripts \
    --no-audit \
    --no-fund \
    "$root_path" "$native_path" >/dev/null || fail "isolated offline npm install failed"

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
"$launcher" version >/dev/null || fail "installed harness-lint version command failed"
"$launcher" --help | grep -Fi 'usage:' >/dev/null || fail "installed harness-lint --help output is incomplete"
"$launcher" hooks status >/dev/null || fail "installed harness-lint hooks status command failed"
npm exec --prefix "$prefix" --userconfig "$user_config" --offline --ignore-scripts --no-audit --no-fund -- harness-lint version >/dev/null || fail "npm exec could not run the local harness-lint command"
echo "npm package E2E passed: audited five packages and executed $version on $(node -p 'process.platform + "/" + process.arch')"
