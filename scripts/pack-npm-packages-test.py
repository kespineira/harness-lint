#!/usr/bin/env python3
"""Focused public-interface tests for pack-npm-packages.sh."""

from __future__ import annotations

import json
import hashlib
import importlib.util
from pathlib import Path
import stat
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "pack-npm-packages.sh"
SCRIPT_PY = ROOT / "scripts" / "pack-npm-packages.py"
MODULE_SPEC = importlib.util.spec_from_file_location("pack_npm_packages", SCRIPT_PY)
assert MODULE_SPEC is not None and MODULE_SPEC.loader is not None
PACK_MODULE = importlib.util.module_from_spec(MODULE_SPEC)
MODULE_SPEC.loader.exec_module(PACK_MODULE)
NATIVES = [
    ("@kespineira/harness-lint-darwin-arm64", "darwin", "arm64"),
    ("@kespineira/harness-lint-darwin-x64", "darwin", "x64"),
    ("@kespineira/harness-lint-linux-arm64", "linux", "arm64"),
    ("@kespineira/harness-lint-linux-x64", "linux", "x64"),
]
VERSION = "1.2.3"


def fixture(
    root: Path,
    version: str = VERSION,
    release_version: str | None = None,
    version_matches_release: bool | None = None,
) -> Path:
    if release_version is None:
        release_version = version
    if version_matches_release is None:
        version_matches_release = version == release_version
    staging = root / "staging"
    staging.mkdir(parents=True)
    native_names = {name for name, _, _ in NATIVES}
    root_manifest = {
        "name": "harness-lint",
        "version": version,
        "bin": {"harness-lint": "bin/harness-lint.js"},
        "files": ["bin/harness-lint.js", "README.md", "LICENSE"],
        "optionalDependencies": {name: version for name in native_names},
    }
    write_package(staging / "harness-lint", root_manifest, {"bin/harness-lint.js": b"#!/usr/bin/env node\n"})
    for name, platform, arch in NATIVES:
        package = staging / name
        manifest = {
            "name": name,
            "version": version,
            "files": ["bin/harness-lint", "README.md", "LICENSE"],
            "os": [platform],
            "cpu": [arch],
        }
        write_package(package, manifest, {"bin/harness-lint": b"native\n"})
    packages = []
    for name, _, _ in [("harness-lint", "", "")] + NATIVES:
        package = staging / name
        files = []
        paths = (
            ["package.json", "bin/harness-lint.js", "README.md", "LICENSE"]
            if name == "harness-lint"
            else ["LICENSE", "README.md", "bin/harness-lint", "package.json"]
        )
        for path in paths:
            file = package / path
            files.append(
                {
                    "path": path,
                    "sha256": hashlib.sha256(file.read_bytes()).hexdigest(),
                    "mode": format(stat.S_IMODE(file.stat().st_mode), "04o"),
                }
            )
        item = {"name": name, "path": name, "files": files}
        if name != "harness-lint":
            binary = package / "bin/harness-lint"
            item["source"] = {
                "binary": {
                    "path": str(binary),
                    "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                }
            }
        packages.append(item)
    # Receipt file arrays are sets of path/hash/mode records, not an ordered
    # serialization contract.
    for item in packages:
        item["files"].reverse()
    (staging / "staging-receipt.json").write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "version": version,
                "releaseVersion": release_version,
                "versionMatchesRelease": version_matches_release,
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


def test_normalize_npm_pack_json() -> None:
    name = "harness-lint"
    record = {"name": name, "files": []}
    assert PACK_MODULE.normalize_npm_pack_json([record], name) == record
    assert PACK_MODULE.normalize_npm_pack_json({name: record}, name) == record

    malformed = [
        [],
        [record, record],
        [None],
        {},
        {name: record, "other": record},
        {"wrong-name": record},
        {name: {"name": "wrong-name"}},
        {name: None},
        {name: {"files": []}},
    ]
    for parsed in malformed:
        try:
            PACK_MODULE.normalize_npm_pack_json(parsed, name)
        except PACK_MODULE.PackError:
            continue
        raise AssertionError(f"accepted malformed npm pack JSON: {parsed!r}")


def run(
    staging: Path,
    output: Path,
    allow_prerelease_version_mismatch: bool = False,
) -> subprocess.CompletedProcess[str]:
    command = [str(SCRIPT), "--staging", str(staging), "--output", str(output)]
    if allow_prerelease_version_mismatch:
        command.append("--allow-prerelease-version-mismatch")
    return subprocess.run(
        command,
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
    assert receipt["stagingReceiptSha256"] == hashlib.sha256(
        (staging / "staging-receipt.json").read_bytes()
    ).hexdigest()
    assert len(list(output.glob("*.tgz"))) == 5
    expected_entries = {
        "package-receipt.json",
        *(name.replace("@", "").replace("/", "-") + "-1.2.3.tgz" for name in expected_order),
    }
    assert {path.name for path in output.iterdir()} == expected_entries
    assert not any(path.is_dir() for path in output.iterdir())
    assert not list(root.glob(".packed.*"))
    assert before == {path: path.read_bytes() for path in staging.rglob("*") if path.is_file()}
    assert not (staging / "harness-lint" / "harness-lint-1.2.3.tgz").exists()
    assert run(staging, output).returncode != 0


def test_dry_run_rejects_missing_and_extra_content(root: Path) -> None:
    staging = fixture(root / "missing")
    (staging / "harness-lint" / "LICENSE").unlink()
    result = run(staging, root / "missing-out")
    assert result.returncode != 0


def test_receipt_detects_unsealed_staged_mutations(root: Path) -> None:
    staging = fixture(root / "native")
    native = staging / NATIVES[0][0] / "bin/harness-lint"
    native.write_bytes(b"mutated native\n")
    result = run(staging, root / "native-out")
    assert result.returncode != 0 and "staged file hash differs from staging receipt" in result.stderr

    staging = fixture(root / "other")
    readme = staging / "harness-lint" / "README.md"
    readme.write_text("mutated readme\n", encoding="utf-8")
    result = run(staging, root / "other-out")
    assert result.returncode != 0 and "staged file hash differs from staging receipt" in result.stderr

    staging = fixture(root / "extra")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["files"].append("extra.txt")
    (staging / "harness-lint" / "extra.txt").write_text("unexpected")
    manifest_path.write_text(json.dumps(manifest))
    result = run(staging, root / "extra-out")
    assert result.returncode != 0 and "staged file hash differs from staging receipt" in result.stderr


def reseal_manifest(staging: Path, package_name: str) -> None:
    receipt_path = staging / "staging-receipt.json"
    receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    package = staging / package_name
    manifest = package / "package.json"
    item = next(item for item in receipt["packages"] if item["name"] == package_name)
    file_entry = next(file for file in item["files"] if file["path"] == "package.json")
    file_entry["sha256"] = hashlib.sha256(manifest.read_bytes()).hexdigest()
    file_entry["mode"] = format(stat.S_IMODE(manifest.stat().st_mode), "04o")
    receipt_path.write_text(json.dumps(receipt), encoding="utf-8")


def test_manifest_policy_diagnostics(root: Path) -> None:
    staging = fixture(root / "version")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["version"] = "1.2.4"
    manifest_path.write_text(json.dumps(manifest))
    reseal_manifest(staging, "harness-lint")
    result = run(staging, root / "version-out")
    assert result.returncode != 0 and "harness-lint manifest has the wrong name or version" in result.stderr
    assert not list(root.glob(".version-out.*"))

    staging = fixture(root / "lifecycle")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["scripts"] = {"install": "echo bad"}
    manifest_path.write_text(json.dumps(manifest))
    reseal_manifest(staging, "harness-lint")
    result = run(staging, root / "lifecycle-out")
    assert result.returncode != 0 and "harness-lint manifest contains lifecycle scripts" in result.stderr

    staging = fixture(root / "bin")
    manifest_path = staging / NATIVES[0][0] / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["bin"] = {"harness-lint": "bin/harness-lint"}
    manifest_path.write_text(json.dumps(manifest))
    reseal_manifest(staging, NATIVES[0][0])
    result = run(staging, root / "bin-out")
    assert result.returncode != 0 and "native manifest must not define a bin field" in result.stderr

    staging = fixture(root / "optional")
    manifest_path = staging / "harness-lint" / "package.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["optionalDependencies"][NATIVES[0][0]] = "^" + VERSION
    manifest_path.write_text(json.dumps(manifest))
    reseal_manifest(staging, "harness-lint")
    result = run(staging, root / "optional-out")
    assert result.returncode != 0 and "root optionalDependencies must contain exact versions" in result.stderr


def test_rejects_version_receipt_and_bootstrap_contracts(root: Path) -> None:
    staging = fixture(root / "receipt")
    receipt_path = staging / "staging-receipt.json"
    receipt = json.loads(receipt_path.read_text())
    receipt["versionMatchesRelease"] = False
    receipt_path.write_text(json.dumps(receipt))
    result = run(staging, root / "receipt-out")
    assert result.returncode != 0 and "versionMatchesRelease" in result.stderr

    staging = fixture(
        root / "bootstrap",
        version="0.0.0-bootstrap.1",
        release_version="0.1.1-SNAPSHOT-abc123",
        version_matches_release=False,
    )
    result = run(
        staging,
        root / "bootstrap-out",
        allow_prerelease_version_mismatch=True,
    )
    assert result.returncode == 0, result.stderr
    receipt = json.loads((root / "bootstrap-out/package-receipt.json").read_text())
    assert receipt["stable"] is False
    assert receipt["versionMatchesRelease"] is False
    assert receipt["bootstrapMode"] == "prerelease-version-mismatch"

    result = run(
        fixture(
            root / "stable",
            version="1.2.4",
            release_version=VERSION,
            version_matches_release=False,
        ),
        root / "stable-out",
        allow_prerelease_version_mismatch=True,
    )
    assert result.returncode != 0 and not (root / "stable-out").exists()

    result = run(
        fixture(
            root / "build-metadata",
            version="0.0.0+bootstrap-build",
            release_version="0.1.1+release-build",
            version_matches_release=False,
        ),
        root / "build-metadata-out",
        allow_prerelease_version_mismatch=True,
    )
    assert result.returncode != 0 and not (root / "build-metadata-out").exists()

    result = run(
        fixture(
            root / "equal",
            version="1.2.3-rc.1",
            release_version="1.2.3-rc.1",
            version_matches_release=True,
        ),
        root / "equal-out",
        allow_prerelease_version_mismatch=True,
    )
    assert result.returncode != 0 and not (root / "equal-out").exists()

    invalid = fixture(
        root / "invalid",
        version="not-semver",
        release_version="1.2.3-rc.1",
        version_matches_release=False,
    )
    result = run(invalid, root / "invalid-out", allow_prerelease_version_mismatch=True)
    assert result.returncode != 0 and not (root / "invalid-out").exists()


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="harness-lint-pack-tests-") as directory:
        root = Path(directory)
        test_normalize_npm_pack_json()
        test_pack_and_root_last(root / "success")
        test_dry_run_rejects_missing_and_extra_content(root / "contents")
        test_receipt_detects_unsealed_staged_mutations(root / "mutations")
        test_manifest_policy_diagnostics(root / "manifest-policy")
        test_rejects_version_receipt_and_bootstrap_contracts(root / "contracts")
    print("pack npm packages tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
