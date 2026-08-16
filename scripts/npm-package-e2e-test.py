#!/usr/bin/env python3
"""Focused isolated-consumer test for npm-package-e2e.sh."""

from __future__ import annotations

import io
import json
from pathlib import Path
import os
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "npm-package-e2e.sh"
VERSION = "1.2.3"
TARGETS = [
    ("darwin", "arm64"),
    ("darwin", "amd64"),
    ("linux", "arm64"),
    ("linux", "amd64"),
]


def write_archive(path: Path, binary: bytes) -> None:
    with tarfile.open(path, "w:gz") as archive:
        for name, contents, mode in (
            ("harness-lint", binary, 0o755),
            ("LICENSE", b"license\n", 0o644),
            ("README.md", b"readme\n", 0o644),
        ):
            info = tarfile.TarInfo(name)
            info.mode = mode
            info.size = len(contents)
            archive.addfile(info, io.BytesIO(contents))


def fixture(root: Path) -> Path:
    dist = root / "dist"
    dist.mkdir(parents=True)
    binary = b"""#!/bin/sh
case "$1" in
  version) printf 'harness-lint version=1.2.3 commit=test build_date=test\\n' ;;
  --help) printf 'Usage: harness-lint [command]\\n' ;;
  hooks) printf '{\"status\":\"ok\"}\\n' ;;
esac
"""
    entries = []
    for goos, goarch in TARGETS:
        binary_path = dist / f"binary-{goos}-{goarch}" / "harness-lint"
        binary_path.parent.mkdir()
        binary_path.write_bytes(binary)
        binary_path.chmod(0o755)
        archive_path = dist / f"archive-{goos}-{goarch}.tar.gz"
        write_archive(archive_path, binary)
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
    (dist / "metadata.json").write_text(json.dumps({"version": VERSION}), encoding="utf-8")
    return dist


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="harness-lint-npm-e2e-tests-") as directory:
        root = Path(directory)
        dist = fixture(root)
        global_root = subprocess.run(
            ["npm", "root", "-g"], capture_output=True, text=True, check=True
        ).stdout.strip()
        before = Path(global_root) / "harness-lint"
        result = subprocess.run(
            [str(SCRIPT), "--dist", str(dist)],
            cwd=ROOT,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
            text=True,
            capture_output=True,
            check=False,
        )
        assert result.returncode == 0, result.stderr
        assert "audited five packages" in result.stdout
        assert not before.exists(), "E2E unexpectedly changed the global npm prefix"
    print("npm package E2E tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
