#!/usr/bin/env python3
"""Deterministic tests for the stable release-output validator."""

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "validate-release-dist.sh"
sys.dont_write_bytecode = True
MODULE_SPEC = importlib.util.spec_from_file_location(
    "validate_release_dist", ROOT / "scripts" / "validate-release-dist.py"
)
assert MODULE_SPEC is not None and MODULE_SPEC.loader is not None
MODULE = importlib.util.module_from_spec(MODULE_SPEC)
MODULE_SPEC.loader.exec_module(MODULE)
VERSION = "1.2.3"
TARGETS = [("darwin", "amd64"), ("darwin", "arm64"), ("linux", "amd64"), ("linux", "arm64")]


def fixture(root: Path) -> Path:
    dist = root / "dist"
    dist.mkdir(parents=True)
    artifacts = []
    distributables: list[Path] = []
    for goos, goarch in TARGETS:
        archive_name = f"harness-lint_{VERSION}_{goos}_{goarch}.tar.gz"
        archive_path = dist / archive_name
        archive_path.write_bytes(f"archive-{goos}-{goarch}".encode())
        distributables.append(archive_path)
        artifacts.append(
            {
                "type": "Archive",
                "name": archive_name,
                "path": f"dist/{archive_name}",
                "goos": goos,
                "goarch": goarch,
                "extra": {"ID": "default"},
            }
        )
        sbom_name = f"{archive_name}.spdx.json"
        sbom_path = dist / sbom_name
        sbom_path.write_text(
            json.dumps(
                {
                    "spdxVersion": "SPDX-2.3",
                    "SPDXID": "SPDXRef-DOCUMENT",
                    "name": archive_name,
                    "documentNamespace": f"https://example.invalid/{sbom_name}",
                    "creationInfo": {"creators": ["Tool: syft-1.51.0"]},
                    "packages": [
                        {"SPDXID": "SPDXRef-project", "name": "github.com/kespineira/harness-lint"},
                        {"SPDXID": "SPDXRef-dependency", "name": "example/dependency"},
                    ],
                    "relationships": [
                        {
                            "spdxElementId": "SPDXRef-dependency",
                            "relatedSpdxElement": "SPDXRef-project",
                            "relationshipType": "DEPENDENCY_OF",
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        distributables.append(sbom_path)
        artifacts.append(
            {
                "type": "SBOM",
                "name": sbom_name,
                "path": f"dist/{sbom_name}",
                "extra": {"ID": "archive-spdx"},
            }
        )
    for package_format in ("deb", "rpm", "apk"):
        for arch in ("amd64", "arm64"):
            package = dist / f"harness-lint_{VERSION}_linux_{arch}.{package_format}"
            package.write_bytes(f"{package_format}-{arch}".encode())
            distributables.append(package)
    (dist / "artifacts.json").write_text(json.dumps(artifacts), encoding="utf-8")
    checksums = [
        f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}"
        for path in distributables
    ]
    (dist / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    return dist


def run(dist: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [str(SCRIPT), "--dist", str(dist), "--version", VERSION],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
        env={**__import__("os").environ, "PYTHONDONTWRITEBYTECODE": "1"},
    )


class ReleaseDistTests(unittest.TestCase):
    def assert_rejected(self, dist: Path) -> None:
        result = run(dist)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("private-fixture-secret", result.stdout + result.stderr)

    def test_accepts_exact_stable_dist(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            result = run(fixture(Path(temporary)))
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_missing_and_extra_artifact(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary) / "missing")
            (dist / f"harness-lint_{VERSION}_linux_arm64.apk").unlink()
            self.assert_rejected(dist)
            dist = fixture(Path(temporary) / "extra")
            (dist / f"harness-lint_{VERSION}_linux_amd64.zip").write_bytes(b"private-fixture-secret")
            self.assert_rejected(dist)

    def test_rejects_malformed_and_duplicate_checksum(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary) / "malformed")
            (dist / "checksums.txt").write_text("private-fixture-secret\n", encoding="utf-8")
            self.assert_rejected(dist)
            dist = fixture(Path(temporary) / "duplicate")
            checksums = (dist / "checksums.txt").read_text(encoding="utf-8").splitlines()
            checksums[-1] = checksums[0]
            (dist / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
            self.assert_rejected(dist)

    def test_rejects_checksum_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary))
            artifact = dist / f"harness-lint_{VERSION}_darwin_amd64.tar.gz"
            artifact.write_bytes(b"private-fixture-secret")
            self.assert_rejected(dist)

    def test_rejects_wrong_or_missing_sbom_and_wrong_platform(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary) / "wrong-sbom")
            sbom = dist / f"harness-lint_{VERSION}_darwin_amd64.tar.gz.spdx.json"
            document = json.loads(sbom.read_text(encoding="utf-8"))
            document["name"] = f"harness-lint_{VERSION}_linux_amd64.tar.gz"
            sbom.write_text(json.dumps(document), encoding="utf-8")
            self.assert_rejected(dist)
            dist = fixture(Path(temporary) / "missing-sbom")
            (dist / f"harness-lint_{VERSION}_linux_amd64.tar.gz.spdx.json").unlink()
            self.assert_rejected(dist)
            dist = fixture(Path(temporary) / "wrong-platform")
            metadata = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            archive = next(entry for entry in metadata if entry.get("type") == "Archive")
            archive["goarch"] = "arm64"
            (dist / "artifacts.json").write_text(json.dumps(metadata), encoding="utf-8")
            self.assert_rejected(dist)


if __name__ == "__main__":
    unittest.main()
