#!/usr/bin/env python3
"""Focused tests for stage-npm-packages.py."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "stage-npm-packages.sh"
TARGETS = [
    ("darwin", "arm64", "x64-do-not-use"),
    ("darwin", "amd64", "darwin-amd64"),
    ("linux", "arm64", "linux-arm64"),
    ("linux", "amd64", "linux-amd64"),
]


def write_tar(path: Path, binary: bytes, extra_name: str | None = None) -> None:
    with tarfile.open(path, "w:gz") as archive:
        for name, contents, mode in (
            ("harness-lint", binary, 0o755),
            ("LICENSE", b"license\n", 0o644),
            ("README.md", b"readme\n", 0o644),
        ):
            info = tarfile.TarInfo(name)
            info.mode = mode
            info.size = len(contents)
            archive.addfile(info, __import__("io").BytesIO(contents))
        if extra_name:
            info = tarfile.TarInfo(extra_name)
            info.mode = 0o644
            info.size = 1
            archive.addfile(info, __import__("io").BytesIO(b"x"))


def fixture(root: Path) -> tuple[Path, list[dict]]:
    root.mkdir(parents=True, exist_ok=True)
    dist = root / "dist"
    dist.mkdir()
    entries: list[dict] = []
    for goos, goarch, marker in TARGETS:
        binary = marker.encode()
        binary_path = dist / f"bin-{goos}-{goarch}" / "harness-lint"
        binary_path.parent.mkdir()
        binary_path.write_bytes(binary)
        binary_path.chmod(0o755)
        archive_path = dist / f"archive-{goos}-{goarch}.tar.gz"
        write_tar(archive_path, binary)
        entries.extend(
            [
                {
                    "name": "harness-lint",
                    "path": str(binary_path.relative_to(dist.parent)),
                    "goos": goos,
                    "goarch": goarch,
                    "type": "Binary",
                    "extra": {"Binary": "harness-lint", "ID": "harness-lint"},
                },
                {
                    "name": archive_path.name,
                    "path": str(archive_path.relative_to(dist.parent)),
                    "goos": goos,
                    "goarch": goarch,
                    "type": "Archive",
                    "extra": {
                        "Binaries": ["harness-lint"],
                        "Files": ["LICENSE", "README.md"],
                        "Format": "tar.gz",
                        "ID": "default",
                    },
                },
            ]
        )
    (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
    return dist, entries


def run(version: str, dist: Path, output: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [str(SCRIPT), "--version", version, "--dist", str(dist), "--output", str(output)],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )


def assert_success(version: str, dist: Path, output: Path) -> dict:
    result = run(version, dist, output)
    assert result.returncode == 0, result.stderr
    receipt = json.loads((output / "staging-receipt.json").read_text(encoding="utf-8"))
    assert receipt["version"] == version
    assert len(receipt["packages"]) == 5
    assert all("__" not in json.dumps(item) for item in receipt["packages"])
    return receipt


def test_success_and_contract(root: Path) -> None:
    dist, _ = fixture(root)
    output = root / "stage-stable"
    receipt = assert_success("1.2.3", dist, output)
    assert set(receipt["assets"]) == {"launcher", "readme", "license"}
    assert all(Path(item["path"]).is_file() for item in receipt["assets"].values())
    package_dirs = [output / "harness-lint"] + sorted((output / "@kespineira").glob("*"))
    assert len(package_dirs) == 5
    for package in package_dirs:
        manifest = json.loads((package / "package.json").read_text(encoding="utf-8"))
        assert manifest["version"] == "1.2.3"
        assert set(manifest.get("files", [])) in (
            {"bin/harness-lint.js", "README.md", "LICENSE"},
            {"bin/harness-lint", "README.md", "LICENSE"},
        )
    root_manifest = json.loads((output / "harness-lint/package.json").read_text(encoding="utf-8"))
    assert set(root_manifest["optionalDependencies"].values()) == {"1.2.3"}
    assert len(root_manifest["optionalDependencies"]) == 4
    for package in package_dirs[1:]:
        binary = package / "bin" / "harness-lint"
        assert stat.S_IMODE(binary.stat().st_mode) == 0o755
    for item in receipt["packages"][1:]:
        source = item["source"]
        assert Path(source["archive"]["path"]).is_file()
        assert Path(source["binary"]["path"]).is_file()
        binary = next(file for file in item["files"] if file["path"] == "bin/harness-lint")
        assert binary["sha256"] == source["binary"]["sha256"]
        assert hashlib.sha256((output / item["path"] / "bin/harness-lint").read_bytes()).hexdigest() == binary["sha256"]


def test_prerelease_and_version_validation(root: Path) -> None:
    dist, _ = fixture(root)
    assert_success("1.2.3-rc.1", dist, root / "stage-prerelease")
    for version in ("v1.2.3", "1.2", "01.2.3", "1.2.3-01", "0.1.2+", ""):
        result = run(version, dist, root / f"invalid-{len(version)}")
        assert result.returncode != 0, version


def test_missing_duplicate_and_wrong_artifacts(root: Path) -> None:
    dist, entries = fixture(root)
    dist.joinpath("artifacts.json").write_text(json.dumps(entries[:-1]), encoding="utf-8")
    result = run("1.2.3", dist, root / "missing")
    assert result.returncode != 0 and not (root / "missing").exists()

    dist, entries = fixture(root / "duplicate")
    entries.append(dict(entries[0]))
    dist.joinpath("artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
    result = run("1.2.3", dist, root / "duplicate-stage")
    assert result.returncode != 0 and "found 2" in result.stderr

    dist, entries = fixture(root / "wrong")
    archive = dist / "archive-linux-amd64.tar.gz"
    write_tar(archive, b"wrong-binary")
    dist.joinpath("artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
    result = run("1.2.3", dist, root / "wrong-stage")
    assert result.returncode != 0 and "hash" in result.stderr


def test_unsafe_archive(root: Path) -> None:
    dist, entries = fixture(root)
    archive = dist / "archive-linux-amd64.tar.gz"
    write_tar(archive, b"linux-amd64", "../escape")
    dist.joinpath("artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
    result = run("1.2.3", dist, root / "unsafe-stage")
    assert result.returncode != 0 and "unsafe" in result.stderr


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="harness-lint-stage-tests-") as temporary:
        root = Path(temporary)
        test_success_and_contract(root / "success")
        test_prerelease_and_version_validation(root / "versions")
        test_missing_duplicate_and_wrong_artifacts(root / "artifacts")
        test_unsafe_archive(root / "unsafe")
    print("stage npm packages tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
