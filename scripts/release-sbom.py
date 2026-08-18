#!/usr/bin/env python3
"""Validate the archive SBOM contract in one GoReleaser dist directory."""

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
ARCHIVE_RE = re.compile(
    r"^harness-lint_(?P<version>.+)_(?P<os>darwin|linux)_(?P<arch>amd64|arm64)\.tar\.gz$"
)


class ValidationError(ValueError):
    """Raised when a release SBOM does not satisfy the distribution contract."""


def fail(message: str) -> None:
    raise ValidationError(f"release SBOM: {message}")


def read_json(path: Path, description: str) -> Any:
    try:
        with path.open(encoding="utf-8") as stream:
            return json.load(stream)
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot parse {description}: {error}")


def require_string(value: Any, description: str) -> str:
    if not isinstance(value, str) or not value:
        fail(f"{description} must be a non-empty string")
    return value


def validate_sboms(dist: Path, version: str) -> list[Path]:
    artifacts_path = dist / "artifacts.json"
    checksums_path = dist / "checksums.txt"
    artifacts = read_json(artifacts_path, "GoReleaser artifacts.json")
    if not isinstance(artifacts, list):
        fail("GoReleaser artifacts.json must contain an array")

    archives: dict[str, dict[str, Any]] = {}
    for entry in artifacts:
        if not isinstance(entry, dict) or entry.get("type") != "Archive":
            continue
        name = entry.get("name")
        if not isinstance(name, str):
            continue
        match = ARCHIVE_RE.fullmatch(name)
        if match and match.group("version") == version:
            target = f"{match.group('os')}/{match.group('arch')}"
            if target in archives:
                fail(f"duplicate canonical archive metadata for {target}")
            if entry.get("extra", {}).get("ID") != "default":
                fail(f"canonical archive {name} is not the default archive")
            if entry.get("goos") != match.group("os") or entry.get("goarch") != match.group("arch"):
                fail(f"archive metadata platform does not match its name: {name}")
            archives[target] = entry

    if set(archives) != set(TARGETS):
        fail(f"canonical archive targets differ from {', '.join(TARGETS)}")

    sbom_paths = sorted(dist.glob("*.spdx.json"))
    expected_names = {f"{entry['name']}.spdx.json" for entry in archives.values()}
    expected_paths = sorted(dist / name for name in expected_names)
    if sbom_paths != expected_paths:
        actual = ", ".join(path.name for path in sbom_paths) or "none"
        expected = ", ".join(path.name for path in expected_paths)
        fail(f"SBOM set differs from the four canonical archives (actual: {actual}; expected: {expected})")

    sbom_entries = {
        entry.get("name"): entry
        for entry in artifacts
        if isinstance(entry, dict) and entry.get("type") == "SBOM"
    }
    if set(sbom_entries) != expected_names:
        fail("artifacts.json SBOM entries do not match the four canonical archive SBOMs")
    for name, entry in sbom_entries.items():
        if entry.get("path") != f"dist/{name}" or entry.get("extra", {}).get("ID") != "archive-spdx":
            fail(f"artifacts.json SBOM metadata does not publish the canonical document: {name}")

    checksum_lines: dict[str, str] = {}
    try:
        lines = checksums_path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        fail(f"cannot read checksums.txt: {error}")
    for line in lines:
        fields = line.split()
        if len(fields) != 2 or not re.fullmatch(r"[0-9a-fA-F]{64}", fields[0]):
            continue
        checksum_lines[fields[1].lstrip("*")] = fields[0].lower()

    for target in TARGETS:
        archive_name = archives[target]["name"]
        archive_path = dist / archive_name
        sbom_path = dist / f"{archive_name}.spdx.json"
        if not archive_path.is_file():
            fail(f"canonical archive is missing: {archive_name}")
        if not sbom_path.is_file():
            fail(f"canonical archive SBOM is missing: {sbom_path.name}")
        document = read_json(sbom_path, sbom_path.name)
        if not isinstance(document, dict):
            fail(f"{sbom_path.name} must contain a JSON object")
        if document.get("spdxVersion") != "SPDX-2.3":
            fail(f"{sbom_path.name} is not SPDX 2.3")
        if document.get("SPDXID") != "SPDXRef-DOCUMENT":
            fail(f"{sbom_path.name} has an invalid SPDX document identifier")
        require_string(document.get("documentNamespace"), f"{sbom_path.name} documentNamespace")
        if require_string(document.get("name"), f"{sbom_path.name} name") != archive_name:
            fail(f"{sbom_path.name} does not identify its canonical archive")
        creation_info = document.get("creationInfo")
        if not isinstance(creation_info, dict):
            fail(f"{sbom_path.name} has no SPDX creationInfo")
        creators = creation_info.get("creators")
        if not isinstance(creators, list) or f"Tool: syft-{SYFT_VERSION}" not in creators:
            fail(f"{sbom_path.name} was not generated by Syft v{SYFT_VERSION}")
        packages = document.get("packages")
        relationships = document.get("relationships")
        if not isinstance(packages, list) or not packages:
            fail(f"{sbom_path.name} has no packages")
        for package in packages:
            if not isinstance(package, dict):
                fail(f"{sbom_path.name} contains an invalid package record")
            require_string(package.get("SPDXID"), f"{sbom_path.name} package SPDXID")
            require_string(package.get("name"), f"{sbom_path.name} package name")
        if not isinstance(relationships, list) or not relationships:
            fail(f"{sbom_path.name} has no dependency relationships")
        dependencies = [
            relationship
            for relationship in relationships
            if isinstance(relationship, dict) and relationship.get("relationshipType") == "DEPENDENCY_OF"
        ]
        if not dependencies:
            fail(f"{sbom_path.name} has no DEPENDENCY_OF relationships")
        project_packages = [
            package
            for package in packages
            if isinstance(package, dict) and package.get("name") == "github.com/kespineira/harness-lint"
        ]
        if not project_packages:
            fail(f"{sbom_path.name} does not identify the distributed harness-lint package")
        digest = hashlib.sha256(sbom_path.read_bytes()).hexdigest()
        if checksum_lines.get(sbom_path.name) != digest:
            fail(f"checksums.txt does not match {sbom_path.name}")

    return [dist / f"{archives[target]['name']}.spdx.json" for target in TARGETS]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        paths = validate_sboms(args.dist, args.version)
    except ValidationError as error:
        print(error, file=sys.stderr)
        return 1
    print(f"validated {len(paths)} SPDX 2.3 archive SBOMs generated by Syft v{SYFT_VERSION}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
