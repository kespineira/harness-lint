#!/usr/bin/env python3
"""Focused tests for release-sbom.py."""

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "release-sbom.py"
SPEC = importlib.util.spec_from_file_location("release_sbom", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


TARGETS = [("darwin", "amd64"), ("darwin", "arm64"), ("linux", "amd64"), ("linux", "arm64")]
VERSION = "1.2.3"


def fixture(root: Path) -> Path:
    dist = root / "dist"
    dist.mkdir()
    artifacts = []
    checksums = []
    for goos, goarch in TARGETS:
        archive_name = f"harness-lint_{VERSION}_{goos}_{goarch}.tar.gz"
        archive_path = dist / archive_name
        archive_path.write_bytes(b"canonical archive")
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
        sbom_path = dist / f"{archive_name}.spdx.json"
        sbom_path.write_text(
            json.dumps(
                {
                    "spdxVersion": "SPDX-2.3",
                    "SPDXID": "SPDXRef-DOCUMENT",
                    "name": archive_name,
                    "documentNamespace": f"https://example.invalid/{archive_name}",
                    "creationInfo": {
                        "created": "2026-08-18T00:00:00Z",
                        "creators": ["Tool: syft-1.51.0"],
                    },
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
        artifacts.append(
            {
                "type": "SBOM",
                "name": sbom_path.name,
                "path": f"dist/{sbom_path.name}",
                "extra": {"ID": "archive-spdx"},
            }
        )
        checksums.append(f"{hashlib.sha256(sbom_path.read_bytes()).hexdigest()}  {sbom_path.name}")
    (dist / "artifacts.json").write_text(json.dumps(artifacts), encoding="utf-8")
    (dist / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    return dist


class ReleaseSBOMTests(unittest.TestCase):
    def test_validates_four_canonical_archive_sboms(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            paths = MODULE.validate_sboms(fixture(Path(temporary)), VERSION)
            self.assertEqual(len(paths), 4)
            self.assertTrue(all(path.name.endswith(".tar.gz.spdx.json") for path in paths))

    def test_rejects_wrong_syft_pin(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary))
            sbom = next(dist.glob("*_linux_amd64.tar.gz.spdx.json"))
            document = json.loads(sbom.read_text(encoding="utf-8"))
            document["creationInfo"]["creators"] = ["Tool: syft-1.50.0"]
            sbom.write_text(json.dumps(document), encoding="utf-8")
            with self.assertRaises(MODULE.ValidationError):
                MODULE.validate_sboms(dist, VERSION)

    def test_rejects_extra_sbom(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = fixture(Path(temporary))
            (dist / "repository.spdx.json").write_text("{}", encoding="utf-8")
            with self.assertRaises(MODULE.ValidationError):
                MODULE.validate_sboms(dist, VERSION)


if __name__ == "__main__":
    unittest.main()
