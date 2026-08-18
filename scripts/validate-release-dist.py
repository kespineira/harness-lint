#!/usr/bin/env python3
"""Validate the stable GoReleaser output without rebuilding or executing it."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
from typing import Any


SYFT_VERSION = "1.51.0"
TARGETS = ("darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64")
PACKAGE_FORMATS = ("deb", "rpm", "apk")
ARCHIVE_RE = re.compile(
    r"^harness-lint_(?P<version>.+)_(?P<os>darwin|linux)_(?P<arch>amd64|arm64)\.tar\.gz$"
)


class ValidationError(ValueError):
    """Raised when stable release output violates the distribution contract."""


def fail(message: str) -> None:
    raise ValidationError(f"release dist: {message}")


def expected_artifacts(version: str) -> set[str]:
    archives = {
        f"harness-lint_{version}_{goos}_{goarch}.tar.gz"
        for goos, goarch in (target.split("/") for target in TARGETS)
    }
    names = set(archives)
    names.update(f"{name}.spdx.json" for name in archives)
    names.update(
        f"harness-lint_{version}_linux_{arch}.{package_format}"
        for package_format in PACKAGE_FORMATS
        for arch in ("amd64", "arm64")
    )
    return names


def is_distributable_candidate(name: str) -> bool:
    return (
        "_windows_" in name
        or name.endswith((".tar.gz", ".spdx.json", ".deb", ".rpm", ".apk", ".zip", ".exe"))
    )


def read_json(path: Path, description: str) -> Any:
    try:
        with path.open(encoding="utf-8") as stream:
            return json.load(stream)
    except (OSError, json.JSONDecodeError):
        fail(f"cannot parse {description}")


def require_string(value: Any, description: str) -> str:
    if not isinstance(value, str) or not value:
        fail(f"{description} must be a non-empty string")
    return value


def validate_artifacts_metadata(dist: Path, version: str, expected: set[str]) -> None:
    artifacts = read_json(dist / "artifacts.json", "GoReleaser artifacts metadata")
    if not isinstance(artifacts, list):
        fail("GoReleaser artifacts metadata must contain an array")

    archives: dict[str, dict[str, Any]] = {}
    for entry in artifacts:
        if not isinstance(entry, dict) or entry.get("type") != "Archive":
            continue
        name = entry.get("name")
        if not isinstance(name, str):
            continue
        match = ARCHIVE_RE.fullmatch(name)
        if not match or match.group("version") != version:
            continue
        target = f"{match.group('os')}/{match.group('arch')}"
        if target in archives:
            fail(f"duplicate archive metadata for {target}")
        archives[target] = entry

    if set(archives) != set(TARGETS):
        fail("archive metadata does not cover the four supported platforms")
    for target, entry in archives.items():
        name = entry["name"]
        match = ARCHIVE_RE.fullmatch(name)
        assert match is not None
        if entry.get("path") != f"dist/{name}" or entry.get("extra", {}).get("ID") != "default":
            fail(f"archive metadata is not canonical: {name}")
        if entry.get("goos") != match.group("os") or entry.get("goarch") != match.group("arch"):
            fail(f"archive metadata platform does not match its name: {name}")

    sbom_entries: dict[str, dict[str, Any]] = {}
    for entry in artifacts:
        if not isinstance(entry, dict) or entry.get("type") != "SBOM":
            continue
        name = entry.get("name")
        if isinstance(name, str) and name in sbom_entries:
            fail(f"duplicate SBOM metadata: {name}")
        if isinstance(name, str):
            sbom_entries[name] = entry
    expected_sboms = {f"{name}.spdx.json" for name in expected if name.endswith(".tar.gz")}
    if set(sbom_entries) != expected_sboms:
        fail("SBOM metadata does not cover the four canonical archive documents")
    for name, entry in sbom_entries.items():
        if entry.get("path") != f"dist/{name}" or entry.get("extra", {}).get("ID") != "archive-spdx":
            fail(f"SBOM metadata is not canonical: {name}")


def validate_checksums(dist: Path, expected: set[str]) -> None:
    checksums_path = dist / "checksums.txt"
    try:
        lines = checksums_path.read_text(encoding="utf-8").splitlines()
    except OSError:
        fail("checksums.txt is missing or unreadable")

    checksums: dict[str, str] = {}
    for line in lines:
        fields = line.split()
        if len(fields) != 2 or not re.fullmatch(r"[0-9a-fA-F]{64}", fields[0]):
            fail("checksums.txt contains a malformed entry")
        name = fields[1][1:] if fields[1].startswith("*") else fields[1]
        if name in checksums:
            fail(f"checksums.txt contains a duplicate entry: {name}")
        checksums[name] = fields[0].lower()

    if set(checksums) != expected or len(checksums) != 14:
        fail("checksums.txt must cover exactly fourteen distributable artifacts once")

    for name in sorted(expected):
        path = dist / name
        if not path.is_file():
            fail(f"distributable artifact is missing: {name}")
        actual = hashlib.sha256(path.read_bytes()).hexdigest()
        if checksums[name] != actual:
            fail(f"checksum mismatch for {name}")


def validate_sbom(path: Path, archive_name: str) -> None:
    document = read_json(path, path.name)
    if not isinstance(document, dict):
        fail(f"SBOM is not a JSON object: {path.name}")
    if document.get("spdxVersion") != "SPDX-2.3":
        fail(f"SBOM is not SPDX 2.3: {path.name}")
    if document.get("SPDXID") != "SPDXRef-DOCUMENT":
        fail(f"SBOM has an invalid document identifier: {path.name}")
    require_string(document.get("documentNamespace"), f"SBOM namespace ({path.name})")
    if document.get("name") != archive_name:
        fail(f"SBOM does not correspond to its archive: {path.name}")

    creation_info = document.get("creationInfo")
    if not isinstance(creation_info, dict):
        fail(f"SBOM has no creationInfo: {path.name}")
    creators = creation_info.get("creators")
    if not isinstance(creators, list) or f"Tool: syft-{SYFT_VERSION}" not in creators:
        fail(f"SBOM was not generated by Syft v{SYFT_VERSION}: {path.name}")

    packages = document.get("packages")
    if not isinstance(packages, list) or not packages:
        fail(f"SBOM has no packages: {path.name}")
    for package in packages:
        if not isinstance(package, dict):
            fail(f"SBOM contains an invalid package record: {path.name}")
        require_string(package.get("SPDXID"), f"SBOM package SPDXID ({path.name})")
        require_string(package.get("name"), f"SBOM package name ({path.name})")
    if not any(
        isinstance(package, dict) and package.get("name") == "github.com/kespineira/harness-lint"
        for package in packages
    ):
        fail(f"SBOM does not identify the distributed harness-lint package: {path.name}")

    relationships = document.get("relationships")
    if not isinstance(relationships, list) or not relationships:
        fail(f"SBOM has no relationships: {path.name}")
    if not any(
        isinstance(relationship, dict)
        and relationship.get("relationshipType") == "DEPENDENCY_OF"
        for relationship in relationships
    ):
        fail(f"SBOM has no dependency relationships: {path.name}")


def validate_sboms(dist: Path, version: str) -> None:
    for goos, goarch in (target.split("/") for target in TARGETS):
        archive_name = f"harness-lint_{version}_{goos}_{goarch}.tar.gz"
        archive_path = dist / archive_name
        sbom_path = dist / f"{archive_name}.spdx.json"
        if not archive_path.is_file():
            fail(f"archive is missing: {archive_name}")
        if not sbom_path.is_file():
            fail(f"matching SBOM is missing: {sbom_path.name}")
        validate_sbom(sbom_path, archive_name)


def validate_dist(dist: Path, version: str) -> None:
    if not dist.is_dir():
        fail(f"dist directory is missing: {dist.name}")
    expected = expected_artifacts(version)
    actual = {
        path.name
        for path in dist.iterdir()
        if path.is_file() and is_distributable_candidate(path.name)
    }
    if actual != expected:
        missing = sorted(expected - actual)
        extra = sorted(actual - expected)
        details = []
        if missing:
            details.append(f"missing: {', '.join(missing)}")
        if extra:
            details.append(f"unexpected: {', '.join(extra)}")
        fail("distributable artifact set is not exactly four archives, four SBOMs, and six Linux packages (" + "; ".join(details) + ")")
    validate_artifacts_metadata(dist, version, expected)
    validate_checksums(dist, expected)
    validate_sboms(dist, version)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        validate_dist(args.dist, args.version)
    except ValidationError as error:
        print(error, file=sys.stderr)
        return 1
    print("validated stable dist: four archives, four SPDX 2.3 SBOMs, six Linux packages, and fourteen checksums")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
