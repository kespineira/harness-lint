#!/usr/bin/env python3
"""Stage the npm packages from one canonical GoReleaser output.

This deliberately does not build, download, or invoke npm.  GoReleaser's
artifacts.json is the source of truth for both the target mapping and the
archive/binary correspondence.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import sys
import tarfile
import tempfile
from typing import Any


SEMVER_IDENTIFIER = r"(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION_RE = re.compile(
    rf"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    rf"(?:-(?:{SEMVER_IDENTIFIER})(?:\.(?:{SEMVER_IDENTIFIER}))*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
PLACEHOLDER_RE = re.compile(r"__[A-Z][A-Z0-9_]*__|\{\{.*?\}\}")

EXPECTED_TARGETS = {
    ("darwin", "arm64"): ("@kespineira/harness-lint-darwin-arm64", "darwin", "arm64"),
    ("darwin", "amd64"): ("@kespineira/harness-lint-darwin-x64", "darwin", "x64"),
    ("linux", "arm64"): ("@kespineira/harness-lint-linux-arm64", "linux", "arm64"),
    ("linux", "amd64"): ("@kespineira/harness-lint-linux-x64", "linux", "x64"),
}
ALLOWED_ARCHIVE_MEMBERS = {"harness-lint", "LICENSE", "README.md"}
ROOT_FILES = {"package.json", "bin/harness-lint.js", "README.md", "LICENSE"}
NATIVE_FILES = {"package.json", "bin/harness-lint", "README.md", "LICENSE"}


def has_prerelease(version: str) -> bool:
    core = version.split("+", 1)[0]
    return "-" in core and bool(core.split("-", 1)[1])


class StageError(Exception):
    pass


def fail(message: str) -> None:
    raise StageError(message)


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


def ensure_regular(path: Path, description: str) -> None:
    try:
        mode = path.lstat()
    except OSError as error:
        fail(f"{description} is missing: {path}: {error}")
    if stat.S_ISLNK(mode.st_mode) or not stat.S_ISREG(mode.st_mode):
        fail(f"{description} is not a regular file: {path}")


def ensure_inside(path: Path, root: Path, description: str) -> Path:
    try:
        relative = path.resolve(strict=False).relative_to(root.resolve())
    except ValueError:
        fail(f"{description} escapes dist directory: {path}")
    if relative == Path("."):
        fail(f"{description} points at the dist directory: {path}")
    # Reject symlinked path components, not just a symlink at the leaf.
    current = root.resolve()
    for component in relative.parts:
        current = current / component
        try:
            if current.is_symlink():
                fail(f"{description} uses a symlinked path: {path}")
        except OSError as error:
            fail(f"cannot inspect {description} {path}: {error}")
    return path


def artifact_path(raw: Any, dist: Path, description: str) -> Path:
    if not isinstance(raw, str) or not raw or "\x00" in raw:
        fail(f"{description} has an invalid path")
    raw_path = Path(raw)
    if raw_path.is_absolute():
        candidate = raw_path
    else:
        # GoReleaser records paths relative to the project root ("dist/foo"),
        # while fixtures and future versions may record paths relative to dist.
        parts = raw_path.parts
        if parts and parts[0] == dist.name:
            candidate = dist.parent / raw_path
        else:
            candidate = dist / raw_path
    candidate = ensure_inside(candidate, dist, description)
    ensure_regular(candidate, description)
    return candidate


def validate_version(version: str) -> None:
    if version.startswith("v") or not VERSION_RE.fullmatch(version):
        fail(
            f"invalid npm version {version!r}; provide an explicit semver without a leading v"
        )


def validate_release_version(version: Any) -> str:
    if not isinstance(version, str) or not version:
        fail("GoReleaser metadata version must be a non-empty string")
    if version.startswith("v") or not VERSION_RE.fullmatch(version):
        fail(f"invalid GoReleaser release version {version!r}")
    return version


def require_string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value:
        fail(f"artifact metadata field {name} must be a non-empty string")
    return value


def validate_metadata(metadata: Any) -> list[dict[str, str]]:
    if not isinstance(metadata, dict):
        fail("npm metadata must be an object")
    placeholder = metadata.get("versionPlaceholder")
    if placeholder != "__VERSION__":
        fail("npm metadata versionPlaceholder must be __VERSION__")
    templates = metadata.get("templates")
    assets = metadata.get("assets")
    root = metadata.get("root")
    native = metadata.get("native")
    if not isinstance(templates, dict) or not isinstance(assets, dict):
        fail("npm metadata templates and assets must be objects")
    if not isinstance(root, dict) or not isinstance(native, list):
        fail("npm metadata root and native entries are required")
    for key in ("root", "native"):
        require_string(templates.get(key), f"templates.{key}")
    for key in ("readme", "license"):
        require_string(assets.get(key), f"assets.{key}")
    root_name = require_string(root.get("name"), "root.name")
    if root_name != "harness-lint":
        fail(f"unexpected root npm package name {root_name!r}")
    if len(native) != len(EXPECTED_TARGETS):
        fail(f"npm metadata must describe exactly {len(EXPECTED_TARGETS)} native targets")
    result: list[dict[str, str]] = []
    seen: set[tuple[str, str]] = set()
    for index, item in enumerate(native):
        if not isinstance(item, dict):
            fail(f"npm metadata native[{index}] must be an object")
        values = {
            key: require_string(item.get(key), f"native[{index}].{key}")
            for key in ("name", "os", "cpu", "goos", "goarch")
        }
        key = (values["goos"], values["goarch"])
        if key in seen or key not in EXPECTED_TARGETS:
            fail(f"unexpected or duplicate native target {key[0]}/{key[1]}")
        expected = EXPECTED_TARGETS[key]
        if (values["name"], values["os"], values["cpu"]) != expected:
            fail(f"npm metadata mapping is wrong for {key[0]}/{key[1]}")
        seen.add(key)
        result.append(values)
    if seen != set(EXPECTED_TARGETS):
        fail("npm metadata is missing a supported native target")
    return result


def find_artifact(entries: list[Any], kind: str, goos: str, goarch: str) -> dict[str, Any]:
    matches = [
        entry
        for entry in entries
        if isinstance(entry, dict)
        and entry.get("type") == kind
        and entry.get("goos") == goos
        and entry.get("goarch") == goarch
    ]
    if len(matches) != 1:
        fail(f"expected one {kind} artifact for {goos}/{goarch}, found {len(matches)}")
    return matches[0]


def verify_artifact_pair(
    entries: list[Any], dist: Path, target: dict[str, str]
) -> tuple[Path, Path, str, str, list[dict[str, Any]]]:
    goos, goarch = target["goos"], target["goarch"]
    archive = find_artifact(entries, "Archive", goos, goarch)
    binary = find_artifact(entries, "Binary", goos, goarch)
    archive_extra = archive.get("extra")
    binary_extra = binary.get("extra")
    if not isinstance(archive_extra, dict):
        fail(f"archive metadata for {goos}/{goarch} has no extra object")
    if not isinstance(binary_extra, dict):
        fail(f"binary metadata for {goos}/{goarch} has no extra object")
    if archive_extra.get("ID") != "default" or archive_extra.get("Format") != "tar.gz":
        fail(f"archive for {goos}/{goarch} is not the canonical tar.gz archive")
    if archive_extra.get("Binaries") != ["harness-lint"]:
        fail(f"archive for {goos}/{goarch} does not identify harness-lint")
    if archive_extra.get("Files") != ["LICENSE", "README.md"]:
        fail(f"archive for {goos}/{goarch} has an unexpected file allowlist")
    if binary_extra.get("ID") != "harness-lint" or binary_extra.get("Binary") != "harness-lint":
        fail(f"binary for {goos}/{goarch} is not the canonical harness-lint binary")
    archive_path = artifact_path(archive.get("path"), dist, f"archive {goos}/{goarch}")
    binary_path = artifact_path(binary.get("path"), dist, f"binary {goos}/{goarch}")
    if archive_path == binary_path:
        fail(f"archive and binary paths are identical for {goos}/{goarch}")
    binary_mode = binary_path.stat().st_mode
    if not binary_mode & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH):
        fail(f"canonical binary is not executable for {goos}/{goarch}")
    binary_hash = sha256(binary_path)
    members: list[dict[str, Any]] = []
    names: set[str] = set()
    try:
        with tarfile.open(archive_path, mode="r:gz") as archive_file:
            for member in archive_file.getmembers():
                name = member.name
                posix = PurePosixPath(name)
                if (
                    not name
                    or name.startswith("/")
                    or "\\" in name
                    or any(part in ("", ".", "..") for part in posix.parts)
                    or name != posix.as_posix()
                    or name not in ALLOWED_ARCHIVE_MEMBERS
                ):
                    fail(f"unsafe or unapproved member {name!r} in {archive_path}")
                if name in names:
                    fail(f"duplicate archive member {name!r} in {archive_path}")
                names.add(name)
                if not member.isreg():
                    fail(f"archive member {name!r} is not a regular file")
                if name == "harness-lint":
                    if not member.mode & 0o111:
                        fail(f"archive binary is not executable for {goos}/{goarch}")
                elif member.mode & 0o111:
                    fail(f"archive documentation is executable: {name!r}")
                extracted = archive_file.extractfile(member)
                if extracted is None:
                    fail(f"cannot read archive member {name!r}")
                contents = extracted.read()
                members.append({"name": name, "sha256": hashlib.sha256(contents).hexdigest()})
                if name == "harness-lint" and members[-1]["sha256"] != binary_hash:
                    fail(f"archive binary hash does not match canonical GoReleaser binary for {goos}/{goarch}")
    except (OSError, tarfile.TarError) as error:
        fail(f"cannot inspect archive {archive_path}: {error}")
    if names != ALLOWED_ARCHIVE_MEMBERS:
        fail(f"archive {archive_path} must contain exactly harness-lint, LICENSE, and README.md")
    return archive_path, binary_path, sha256(archive_path), binary_hash, members


def copy_asset(source: Path, destination: Path, description: str) -> str:
    ensure_regular(source, description)
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)
    return sha256(destination)


def render_template(template: Path, replacements: dict[str, str], destination: Path) -> dict[str, Any]:
    ensure_regular(template, "npm template")
    text = template.read_text(encoding="utf-8")
    for placeholder, value in replacements.items():
        text = text.replace(placeholder, value)
    if PLACEHOLDER_RE.search(text):
        fail(f"unresolved placeholder in rendered manifest {destination}")
    try:
        manifest = json.loads(text)
    except json.JSONDecodeError as error:
        fail(f"rendered manifest {destination} is invalid JSON: {error}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(text, encoding="utf-8")
    return manifest


def check_package_files(package: Path, allowed: set[str]) -> None:
    found: set[str] = set()
    for path in package.rglob("*"):
        if path.is_symlink():
            fail(f"staged package contains a symlink: {path}")
        if path.is_file():
            found.add(path.relative_to(package).as_posix())
    if found != allowed:
        fail(f"staged package {package} contents differ from allowlist: {sorted(found)}")


def file_receipt(package: Path, relative: str) -> dict[str, Any]:
    path = package / relative
    ensure_regular(path, "staged file")
    return {
        "path": relative,
        "sha256": sha256(path),
        "mode": format(stat.S_IMODE(path.stat().st_mode), "04o"),
    }


def stage(args: argparse.Namespace) -> Path:
    validate_version(args.version)
    project_root = Path(__file__).resolve().parent.parent
    npm_root = project_root / "npm"
    dist = Path(args.dist).expanduser().resolve()
    if not dist.is_dir():
        fail(f"dist input is not a directory: {dist}")
    artifacts_path = dist / "artifacts.json"
    ensure_regular(artifacts_path, "GoReleaser artifacts.json")
    entries = read_json(artifacts_path)
    if not isinstance(entries, list):
        fail("GoReleaser artifacts.json must contain an array")
    release_metadata_path = dist / "metadata.json"
    ensure_regular(release_metadata_path, "GoReleaser metadata.json")
    release_metadata = read_json(release_metadata_path)
    if not isinstance(release_metadata, dict):
        fail("GoReleaser metadata.json must contain an object")
    release_version = validate_release_version(release_metadata.get("version"))
    version_matches_release = args.version == release_version
    if not version_matches_release:
        if not args.allow_version_mismatch:
            fail(
                "requested npm version does not match GoReleaser release version "
                f"({args.version!r} != {release_version!r}); "
                "pass --allow-version-mismatch only for non-stable snapshot/bootstrap staging"
            )
        if not has_prerelease(args.version) or not has_prerelease(release_version):
            fail("--allow-version-mismatch is only permitted for non-stable snapshot/bootstrap versions")
    metadata = read_json(npm_root / "metadata.json")
    native_specs = validate_metadata(metadata)
    templates = metadata["templates"]
    assets = metadata["assets"]
    root_template = npm_root / templates["root"]
    native_template = npm_root / templates["native"]
    readme = npm_root / assets["readme"]
    license_file = npm_root / assets["license"]
    launcher = npm_root / "harness-lint" / "bin" / "harness-lint.js"
    for path, description in (
        (root_template, "root npm template"),
        (native_template, "native npm template"),
        (readme, "npm README"),
        (license_file, "npm LICENSE"),
        (launcher, "npm launcher"),
    ):
        ensure_regular(path, description)
    asset_sources = {
        "launcher": {"path": str(launcher), "sha256": sha256(launcher)},
        "readme": {"path": str(readme), "sha256": sha256(readme)},
        "license": {"path": str(license_file), "sha256": sha256(license_file)},
    }
    output = Path(args.output).expanduser().resolve()
    if output == dist:
        fail("staging output must be separate from the dist input directory")
    if output.exists():
        fail(f"staging output already exists: {output}")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))
    try:
        root_package = temporary / "harness-lint"
        root_package.mkdir()
        root_manifest = render_template(
            root_template,
            {"__VERSION__": args.version},
            root_package / "package.json",
        )
        if root_manifest.get("name") != "harness-lint" or root_manifest.get("version") != args.version:
            fail("root manifest has the wrong name or version")
        optional = root_manifest.get("optionalDependencies")
        expected_names = {spec["name"] for spec in native_specs}
        if optional != {name: args.version for name in expected_names}:
            fail("root optionalDependencies must contain exact versions for all native packages")
        copy_asset(launcher, root_package / "bin" / "harness-lint.js", "npm launcher")
        root_readme_hash = copy_asset(readme, root_package / "README.md", "npm README")
        root_license_hash = copy_asset(license_file, root_package / "LICENSE", "npm LICENSE")
        check_package_files(root_package, ROOT_FILES)

        packages: list[dict[str, Any]] = [
            {
                "name": "harness-lint",
                "path": "harness-lint",
                "target": None,
                "files": [
                    file_receipt(root_package, "package.json"),
                    file_receipt(root_package, "bin/harness-lint.js"),
                    {"path": "README.md", "sha256": root_readme_hash, "mode": format(stat.S_IMODE((root_package / "README.md").stat().st_mode), "04o")},
                    {"path": "LICENSE", "sha256": root_license_hash, "mode": format(stat.S_IMODE((root_package / "LICENSE").stat().st_mode), "04o")},
                ],
            }
        ]
        for spec in native_specs:
            archive_path, binary_path, archive_hash, binary_hash, archive_members = verify_artifact_pair(
                entries, dist, spec
            )
            package = temporary / spec["name"]
            package.mkdir(parents=True)
            manifest = render_template(
                native_template,
                {
                    "__PACKAGE_NAME__": spec["name"],
                    "__VERSION__": args.version,
                    "__OS__": spec["os"],
                    "__CPU__": spec["cpu"],
                },
                package / "package.json",
            )
            if manifest.get("name") != spec["name"] or manifest.get("version") != args.version:
                fail(f"native manifest has the wrong name or version for {spec['goos']}/{spec['goarch']}")
            native_binary = package / "bin" / "harness-lint"
            native_binary.parent.mkdir()
            with tarfile.open(archive_path, mode="r:gz") as archive_file:
                member = archive_file.getmember("harness-lint")
                extracted = archive_file.extractfile(member)
                if extracted is None:
                    fail(f"cannot extract canonical binary for {spec['goos']}/{spec['goarch']}")
                native_binary.write_bytes(extracted.read())
            os.chmod(native_binary, 0o755)
            if sha256(native_binary) != binary_hash:
                fail(f"staged binary hash changed for {spec['goos']}/{spec['goarch']}")
            copy_asset(readme, package / "README.md", "npm README")
            copy_asset(license_file, package / "LICENSE", "npm LICENSE")
            check_package_files(package, NATIVE_FILES)
            packages.append(
                {
                    "name": spec["name"],
                    "path": spec["name"],
                    "target": {
                        "node": {"os": spec["os"], "cpu": spec["cpu"]},
                        "go": {"os": spec["goos"], "arch": spec["goarch"]},
                    },
                    "source": {
                        "archive": {"path": str(archive_path), "sha256": archive_hash, "members": archive_members},
                        "binary": {"path": str(binary_path), "sha256": binary_hash},
                    },
                    "files": [file_receipt(package, relative) for relative in sorted(NATIVE_FILES)],
                }
            )
        receipt = {
            "schemaVersion": 1,
            "version": args.version,
            "releaseVersion": release_version,
            "versionMatchesRelease": version_matches_release,
            "dist": str(dist),
            "assets": asset_sources,
            "packages": packages,
        }
        receipt_text = json.dumps(receipt, indent=2, sort_keys=True) + "\n"
        if PLACEHOLDER_RE.search(receipt_text):
            fail("unresolved placeholder in staging receipt")
        receipt_path = temporary / "staging-receipt.json"
        try:
            with receipt_path.open("w", encoding="utf-8") as stream:
                stream.write(receipt_text)
                stream.flush()
                os.fsync(stream.fileno())
        except OSError as error:
            fail(f"cannot write staging receipt {receipt_path}: {error}")
        try:
            directory_fd = os.open(temporary, os.O_RDONLY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        except OSError as error:
            fail(f"cannot fsync staging directory {temporary}: {error}")
        os.replace(temporary, output)
        try:
            directory_fd = os.open(output.parent, os.O_RDONLY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        except OSError as error:
            fail(f"cannot fsync staging parent directory {output.parent}: {error}")
        return output / "staging-receipt.json"
    except BaseException:
        shutil.rmtree(temporary, ignore_errors=True)
        raise


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--version", required=True, help="explicit npm semver, without a leading v")
    result.add_argument("--dist", default="dist", help="GoReleaser dist directory (default: dist)")
    result.add_argument(
        "--output",
        default="dist/npm-staging",
        help="new staging directory (default: dist/npm-staging)",
    )
    result.add_argument(
        "--allow-version-mismatch",
        action="store_true",
        help=(
            "allow a non-stable snapshot/bootstrap npm version to differ from "
            "GoReleaser metadata (documented bootstrap path only)"
        ),
    )
    return result


def main() -> int:
    try:
        receipt = stage(parser().parse_args())
        print(f"staged npm packages; receipt: {receipt}")
        return 0
    except StageError as error:
        print(f"stage npm packages: {error}", file=sys.stderr)
        return 1
    except (OSError, KeyError, TypeError, ValueError) as error:
        print(f"stage npm packages: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
