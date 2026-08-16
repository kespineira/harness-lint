#!/usr/bin/env python3
"""Audit and pack the five npm packages from an immutable staging tree.

The staging receipt is the trust boundary for this command.  npm is used only
as a local packer with scripts, audit, funding, and network access disabled;
the resulting tarballs are checked against both npm's dry-run file list and a
second independent tar inspection before the output directory is committed.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from typing import Any


class PackError(Exception):
    pass


NATIVE_ORDER = [
    ("@kespineira/harness-lint-darwin-arm64", "darwin", "arm64"),
    ("@kespineira/harness-lint-darwin-x64", "darwin", "x64"),
    ("@kespineira/harness-lint-linux-arm64", "linux", "arm64"),
    ("@kespineira/harness-lint-linux-x64", "linux", "x64"),
]
PACKAGE_ORDER = [name for name, _, _ in NATIVE_ORDER] + ["harness-lint"]
ROOT_FILES = {"package.json", "bin/harness-lint.js", "README.md", "LICENSE"}
NATIVE_FILES = {"package.json", "bin/harness-lint", "README.md", "LICENSE"}
ROOT_MANIFEST_FILES = ROOT_FILES - {"package.json"}
NATIVE_MANIFEST_FILES = NATIVE_FILES - {"package.json"}
LIFECYCLE_SCRIPTS = {
    "preinstall",
    "install",
    "postinstall",
    "prepare",
    "prepublish",
    "prepublishOnly",
    "postpublish",
    "prepack",
    "postpack",
}


def fail(message: str) -> None:
    raise PackError(message)


def read_json(path: Path) -> Any:
    try:
        with path.open(encoding="utf-8") as stream:
            return json.load(stream)
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read JSON {path}: {error}")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
    except OSError as error:
        fail(f"cannot hash {path}: {error}")
    return digest.hexdigest()


def regular(path: Path, description: str) -> None:
    try:
        mode = path.lstat()
    except OSError as error:
        fail(f"{description} is missing: {path}: {error}")
    if stat.S_ISLNK(mode.st_mode) or not stat.S_ISREG(mode.st_mode):
        fail(f"{description} is not a regular file: {path}")


def package_path(staging: Path, name: str) -> Path:
    # Package names are validated before this function is called, so this is
    # deliberately a simple mapping and cannot interpret traversal components.
    return staging / name


def validate_receipt(staging: Path) -> dict[str, Any]:
    receipt_path = staging / "staging-receipt.json"
    regular(receipt_path, "staging receipt")
    receipt = read_json(receipt_path)
    if not isinstance(receipt, dict) or receipt.get("schemaVersion") != 1:
        fail("staging receipt must be schemaVersion 1")
    version = receipt.get("version")
    release_version = receipt.get("releaseVersion")
    if not isinstance(version, str) or not version:
        fail("staging receipt version must be a non-empty string")
    if version != release_version or receipt.get("versionMatchesRelease") is not True:
        fail("staging receipt is not a stable release (versionMatchesRelease must be true)")
    packages = receipt.get("packages")
    if not isinstance(packages, list) or len(packages) != len(PACKAGE_ORDER):
        fail("staging receipt must describe exactly five packages")
    by_name: dict[str, dict[str, Any]] = {}
    for item in packages:
        if not isinstance(item, dict) or not isinstance(item.get("name"), str):
            fail("staging receipt package entries must have names")
        name = item["name"]
        if name in by_name:
            fail(f"duplicate package in staging receipt: {name}")
        by_name[name] = item
    if set(by_name) != set(PACKAGE_ORDER):
        fail("staging receipt package set does not match the five supported packages")
    for name in PACKAGE_ORDER:
        item = by_name[name]
        relative = item.get("path")
        if relative != name:
            fail(f"staging receipt path for {name} is not its package name")
        package = package_path(staging, relative)
        if not package.is_dir() or package.is_symlink():
            fail(f"staged package directory is missing: {package}")
        regular(package / "package.json", f"{name} manifest")
    # The receipt itself is the only non-package entry allowed in staging.
    allowed = {"staging-receipt.json", "harness-lint", "@kespineira"}
    actual = {path.name for path in staging.iterdir()}
    if actual != allowed:
        fail(f"staging contains unexpected top-level entries: {sorted(actual - allowed)}")
    scope = staging / "@kespineira"
    if not scope.is_dir() or {path.name for path in scope.iterdir()} != {
        name.split("/", 1)[1] for name, _, _ in NATIVE_ORDER
    }:
        fail("staging scope contains unexpected native packages")
    return {"receipt": receipt, "packages": by_name}


def audit_manifest(name: str, package: Path, version: str) -> dict[str, Any]:
    manifest = read_json(package / "package.json")
    if not isinstance(manifest, dict):
        fail(f"{name} manifest must be an object")
    if manifest.get("name") != name or manifest.get("version") != version:
        fail(f"{name} manifest has the wrong name or version")
    scripts = manifest.get("scripts", {})
    if scripts and (not isinstance(scripts, dict) or any(key in LIFECYCLE_SCRIPTS for key in scripts)):
        fail(f"{name} manifest contains lifecycle scripts")
    if "scripts" in manifest and scripts:
        fail(f"{name} manifest contains scripts; package packing is script-free")
    expected_files = ROOT_MANIFEST_FILES if name == "harness-lint" else NATIVE_MANIFEST_FILES
    if set(manifest.get("files", [])) != expected_files:
        fail(f"{name} manifest files field differs from the exact allowlist")
    if name == "harness-lint":
        if manifest.get("bin") != {"harness-lint": "bin/harness-lint.js"}:
            fail("root manifest bin field is not the launcher entry")
        if "os" in manifest or "cpu" in manifest:
            fail("root manifest must not have platform selectors")
        expected_optional = {native: version for native, _, _ in NATIVE_ORDER}
        if manifest.get("optionalDependencies") != expected_optional:
            fail("root optionalDependencies must contain exact versions for all native packages")
    else:
        expected = next((item for item in NATIVE_ORDER if item[0] == name), None)
        assert expected is not None
        _, expected_os, expected_cpu = expected
        if manifest.get("os") != [expected_os] or manifest.get("cpu") != [expected_cpu]:
            fail(f"{name} manifest platform selectors are wrong")
        if "bin" in manifest:
            fail(f"native manifest must not define a bin field: {name}")
        if "optionalDependencies" in manifest:
            fail(f"native manifest must not define optionalDependencies: {name}")
    return manifest


def npm_json(command: list[str], package: Path) -> dict[str, Any]:
    environment = os.environ.copy()
    environment.update(
        {
            "npm_config_audit": "false",
            "npm_config_fund": "false",
            "npm_config_ignore_scripts": "true",
            "npm_config_offline": "true",
            "npm_config_update_notifier": "false",
        }
    )
    try:
        result = subprocess.run(
            command,
            cwd=package,
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
    except OSError as error:
        fail(f"cannot execute npm for {package}: {error}")
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        fail(f"npm pack failed for {package.name}: {detail}")
    try:
        parsed = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        fail(f"npm pack did not produce JSON for {package.name}: {error}")
    if not isinstance(parsed, list) or len(parsed) != 1 or not isinstance(parsed[0], dict):
        fail(f"npm pack JSON for {package.name} is not one package record")
    return parsed[0]


def dry_files(record: dict[str, Any], name: str) -> set[str]:
    files = record.get("files")
    if not isinstance(files, list):
        fail(f"npm dry-run record for {name} has no files list")
    result: set[str] = set()
    for item in files:
        if not isinstance(item, dict) or not isinstance(item.get("path"), str):
            fail(f"npm dry-run record for {name} has an invalid file")
        path = item["path"]
        if path.startswith("package/"):
            path = path[8:]
        result.add(path)
    return result


def audit_tarball(path: Path, name: str, expected: set[str]) -> list[dict[str, Any]]:
    regular(path, f"packed tarball for {name}")
    found: list[dict[str, Any]] = []
    names: set[str] = set()
    try:
        with tarfile.open(path, "r:gz") as archive:
            for member in archive.getmembers():
                if not member.name.startswith("package/"):
                    fail(f"tarball {name} contains an unsafe member {member.name!r}")
                relative = member.name[8:]
                posix = PurePosixPath(relative)
                if (
                    not relative
                    or relative.startswith("/")
                    or "\\" in relative
                    or relative != posix.as_posix()
                    or any(part in ("", ".", "..") for part in posix.parts)
                ):
                    fail(f"tarball {name} contains an unsafe member {member.name!r}")
                if relative in names:
                    fail(f"tarball {name} contains duplicate member {relative!r}")
                names.add(relative)
                if not member.isreg():
                    fail(f"tarball {name} member {relative!r} is not a regular file")
                executable = relative == "bin/harness-lint" or (
                    name == "harness-lint" and relative == "bin/harness-lint.js"
                )
                if name != "harness-lint" and relative == "bin/harness-lint" and not member.mode & 0o111:
                    fail(f"native tarball {name} binary is not executable")
                if not executable and member.mode & 0o111:
                    fail(f"tarball {name} documentation is executable: {relative}")
                extracted = archive.extractfile(member)
                if extracted is None:
                    fail(f"cannot read tarball {name} member {relative!r}")
                found.append({"path": relative, "sha256": hashlib.sha256(extracted.read()).hexdigest()})
    except (OSError, tarfile.TarError) as error:
        fail(f"cannot inspect npm tarball {path}: {error}")
    if names != expected:
        fail(f"tarball {name} contents differ from exact allowlist: {sorted(names)}")
    return sorted(found, key=lambda item: item["path"])


def tree_hashes(root: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink():
            result[str(path.relative_to(root))] = sha256(path)
    return result


def pack(args: argparse.Namespace) -> Path:
    staging = Path(args.staging).expanduser().resolve()
    if not staging.is_dir():
        fail(f"staging input is not a directory: {staging}")
    output = Path(args.output).expanduser().resolve()
    if output.exists():
        fail(f"package output already exists: {output}")
    validated = validate_receipt(staging)
    receipt = validated["receipt"]
    package_entries = validated["packages"]
    version = receipt["version"]
    before = tree_hashes(staging)
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))
    try:
        audited: list[tuple[str, Path, set[str], dict[str, Any]]] = []
        for name in PACKAGE_ORDER:
            package = package_path(staging, name)
            manifest = audit_manifest(name, package, version)
            expected_files = ROOT_FILES if name == "harness-lint" else NATIVE_FILES
            dry = npm_json(
                ["npm", "pack", "--dry-run", "--json", "--ignore-scripts", "--offline", "--no-audit", "--no-fund"],
                package,
            )
            dry_paths = dry_files(dry, name)
            if dry_paths != expected_files:
                fail(f"npm dry-run contents for {name} differ from exact allowlist: {sorted(dry_paths)}")
            audited.append((name, package, dry_paths, dry))
        records: list[dict[str, Any]] = []
        for name, package, dry_paths, dry in audited:
            expected_files = ROOT_FILES if name == "harness-lint" else NATIVE_FILES
            actual = npm_json(
                [
                    "npm",
                    "pack",
                    "--json",
                    "--ignore-scripts",
                    "--offline",
                    "--no-audit",
                    "--no-fund",
                    "--pack-destination",
                    str(temporary),
                ],
                package,
            )
            filename = actual.get("filename")
            if not isinstance(filename, str) or Path(filename).name != filename or not filename.endswith(".tgz"):
                fail(f"npm pack returned an invalid tarball filename for {name}")
            tarball = temporary / filename
            tar_files = audit_tarball(tarball, name, expected_files)
            if {item["path"] for item in tar_files} != dry_paths:
                fail(f"actual tarball contents for {name} differ from npm dry-run contents")
            records.append(
                {
                    "name": name,
                    "version": version,
                    "stagingPath": name,
                    "filename": filename,
                    "sha256": sha256(tarball),
                    "integrity": actual.get("integrity"),
                    "dryRun": {"files": sorted(dry_paths), "npm": dry},
                    "files": tar_files,
                }
            )
        after = tree_hashes(staging)
        if before != after:
            fail("npm packing modified the staging tree")
        package_receipt = {
            "schemaVersion": 1,
            "version": version,
            "stagingReceipt": str(staging / "staging-receipt.json"),
            "packOrder": PACKAGE_ORDER,
            "packages": records,
        }
        receipt_path = temporary / "package-receipt.json"
        receipt_path.write_text(json.dumps(package_receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        os.replace(temporary, output)
        return output / "package-receipt.json"
    except BaseException:
        shutil.rmtree(temporary, ignore_errors=True)
        raise


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--staging", required=True, help="existing staged npm package directory")
    result.add_argument("--output", required=True, help="new directory for audited npm tarballs")
    return result


def main() -> int:
    try:
        receipt = pack(parser().parse_args())
        print(f"packed npm packages; receipt: {receipt}")
        return 0
    except PackError as error:
        print(f"pack npm packages: {error}", file=sys.stderr)
        return 1
    except (OSError, KeyError, TypeError, ValueError) as error:
        print(f"pack npm packages: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
