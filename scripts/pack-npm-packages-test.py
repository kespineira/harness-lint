#!/usr/bin/env python3
"""Focused public-interface tests for pack-npm-packages.sh."""

from __future__ import annotations

import json
from pathlib import Path
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "pack-npm-packages.sh"
NATIVES = [
    ("@kespineira/harness-lint-darwin-arm64", "darwin", "arm64"),
    ("@kespineira/harness-lint-darwin-x64", "darwin", "x64"),
    ("@kespineira/harness-lint-linux-arm64", "linux", "arm64"),
    ("@kespineira/harness-lint-linux-x64", "linux", "x64"),
]
VERSION = "1.2.3"


def fixture(root: Path) -> Path:
    staging = root / "staging"
    staging.mkdir(parents=True)
    native_names = {name for name, _, _ in NATIVES}
    root_manifest = {
        "name": "harness-lint",
        "version": VERSION,
        "bin": {"harness-lint": "bin/harness-lint.js"},
        "files": ["bin/harness-lint.js", "README.md", "LICENSE"],
        "optionalDependencies": {name: VERSION for name in native_names},
    }
    write_package(staging / "harness-lint", root_manifest, {"bin/harness-lint.js": b"#!/usr/bin/env node\n"})
    for name, platform, arch in NATIVES:
        package = staging / name
        manifest = {
            "name": name,
            "version": VERSION,
            "files": ["bin/harness-lint", "README.md", "LICENSE"],
            "os": [platform],
            "cpu": [arch],
        }
        write_package(package, manifest, {"bin/harness-lint": b"native\n"})
    packages = [{"name": "harness-lint", "path": "harness-lint"}]
    packages.extend({"name": name, "path": name} for name, _, _ in NATIVES)
    (staging / "staging-receipt.json").write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "version": VERSION,
                "releaseVersion": VERSION,
                "versionMatchesRelease": True,
                "packages": packages,
            }
        ),
        encoding="utf-8",
    )
    return staging


def write_package(package: Path, manifest: dict, files: dict[str, bytes]) -> None:
    package.mkdir(parents=True)
    (package / "package.json").write_text(json.dumps(manifest), encoding="utf-8")
    (package / "README.md").write_text("readme\n", encoding="utf-8")
    (package / "LICENSE").write_text("license\n", encoding="utf-8")
    for relative, contents in files.items():
        path = package / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(contents)
        path.chmod(0o755)


def run(staging: Path, output: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [str(SCRIPT), "--staging", str(staging), "--output", str(output)],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
        env={**__import__("os").environ, "PYTHONDONTWRITEBYTECODE": "1"},
    )


def test_pack_and_root_last(root: Path) -> None:
    staging = fixture(root)
    before = {path: path.read_bytes() for path in staging.rglob("*") if path.is_file()}
    output = root / "packed"
    result = run(staging, output)
    assert result.returncode == 0, result.stderr
    receipt = json.loads((output / "package-receipt.json").read_text(encoding="utf-8"))
    expected_order = [name for name, _, _ in NATIVES] + ["harness-lint"]
    assert receipt["packOrder"] == expected_order
    assert [item["name"] for item in receipt["packages"]] == expected_order
    assert len(list(output.glob("*.tgz"))) == 5
    assert before == {path: path.read_bytes() for path in staging.rglob("*") if path.is_file()}
    assert not (staging / "harness-lint" / "harness-lint-1.2.3.tgz").exists()
    assert run(staging, output).returncode != 0


def test_dry_run_rejects_missing_and_extra_content(root: Path) -> None:
    staging = fixture(root / "missing")
    (staging / "harness-lint" / "LICENSE").unlink()
    result = run(staging, root / "missing-out")
    assert result.returncode != 0 and "allowlist" in result.stderr

    staging = fixture(root / "extra")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["files"].append("extra.txt")
    (staging / "harness-lint" / "extra.txt").write_text("unexpected")
    manifest_path.write_text(json.dumps(manifest))
    result = run(staging, root / "extra-out")
    assert result.returncode != 0 and "allowlist" in result.stderr


def test_rejects_version_lifecycle_native_bin_and_optional_mismatch(root: Path) -> None:
    staging = fixture(root / "version")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["version"] = "1.2.4"
    manifest_path.write_text(json.dumps(manifest))
    assert run(staging, root / "version-out").returncode != 0

    staging = fixture(root / "receipt")
    receipt_path = staging / "staging-receipt.json"
    receipt = json.loads(receipt_path.read_text())
    receipt["versionMatchesRelease"] = False
    receipt_path.write_text(json.dumps(receipt))
    result = run(staging, root / "receipt-out")
    assert result.returncode != 0 and "versionMatchesRelease" in result.stderr

    staging = fixture(root / "lifecycle")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["scripts"] = {"install": "echo bad"}
    manifest_path.write_text(json.dumps(manifest))
    assert run(staging, root / "lifecycle-out").returncode != 0

    staging = fixture(root / "bin")
    manifest_path = staging / NATIVES[0][0] / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["bin"] = {"harness-lint": "bin/harness-lint"}
    manifest_path.write_text(json.dumps(manifest))
    result = run(staging, root / "bin-out")
    assert result.returncode != 0 and "bin field" in result.stderr

    staging = fixture(root / "optional")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["optionalDependencies"][NATIVES[0][0]] = "^" + VERSION
    manifest_path.write_text(json.dumps(manifest))
    assert run(staging, root / "optional-out").returncode != 0


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="harness-lint-pack-tests-") as directory:
        root = Path(directory)
        test_pack_and_root_last(root / "success")
        test_dry_run_rejects_missing_and_extra_content(root / "contents")
        test_rejects_version_lifecycle_native_bin_and_optional_mismatch(root / "contracts")
    print("pack npm packages tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
