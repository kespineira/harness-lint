#!/usr/bin/env python3
"""Focused deterministic tests for the resumable npm publisher."""

from __future__ import annotations

import importlib.util
import io
import json
from pathlib import Path
import os
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "publish-npm-packages.py"
spec = importlib.util.spec_from_file_location("publish_npm_packages", SCRIPT)
assert spec and spec.loader
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def test_security_helpers() -> None:
    assert module.repository_identity("git+https://github.com/kespineira/harness-lint.git") == module.repository_identity(
        "https://github.com/kespineira/harness-lint"
    )
    assert module.derive_version("v1.2.3") == "1.2.3"
    for bad in ("1.2.3", "vv1.2.3", "v01.2.3", "v1.2.3-rc.1"):
        try:
            module.derive_version(bad)
        except module.PublishError:
            pass
        else:
            raise AssertionError(f"accepted invalid stable tag {bad}")
    with tempfile.TemporaryDirectory(prefix="harness-lint-publish-env-") as directory:
        old_token = os.environ.get("NPM_TOKEN")
        old_oidc = os.environ.get("ACTIONS_ID_TOKEN_REQUEST_URL")
        os.environ["NPM_TOKEN"] = "must-not-leak"
        os.environ["ACTIONS_ID_TOKEN_REQUEST_URL"] = "https://actions.example/oidc"
        try:
            environment = module.clean_npm_environment(Path(directory))
        finally:
            if old_token is None:
                os.environ.pop("NPM_TOKEN", None)
            else:
                os.environ["NPM_TOKEN"] = old_token
            if old_oidc is None:
                os.environ.pop("ACTIONS_ID_TOKEN_REQUEST_URL", None)
            else:
                os.environ["ACTIONS_ID_TOKEN_REQUEST_URL"] = old_oidc
        assert "NPM_TOKEN" not in environment
        assert environment["ACTIONS_ID_TOKEN_REQUEST_URL"] == "https://actions.example/oidc"
        assert environment["NPM_CONFIG_USERCONFIG"].endswith("npmrc")


def test_tarball_manifest_and_receipt_filename() -> None:
    with tempfile.TemporaryDirectory(prefix="harness-lint-publish-tests-") as directory:
        root = Path(directory)
        tarball = root / "harness-lint-1.2.3.tgz"
        manifest = {
            "name": "harness-lint",
            "version": "1.2.3",
            "repository": {"type": "git", "url": "git+https://github.com/kespineira/harness-lint.git"},
            "optionalDependencies": {name: "1.2.3" for name, _, _ in module.NATIVE_ORDER},
        }
        with tarfile.open(tarball, "w:gz") as archive:
            payload = json.dumps(manifest).encode()
            info = tarfile.TarInfo("package/package.json")
            info.size = len(payload)
            archive.addfile(info, io.BytesIO(payload))
        assert module.manifest_from_tarball(tarball, "harness-lint", "1.2.3")["name"] == "harness-lint"
        receipt = {
            "schemaVersion": 1,
            "version": "1.2.3",
            "releaseVersion": "1.2.3",
            "stable": True,
            "packages": [
                {
                    "name": name,
                    "filename": ("wrong.tgz" if index == 0 else f"{name.replace('@', '').replace('/', '-')}-1.2.3.tgz"),
                    "sha256": "0" * 64,
                    "integrity": "sha512-invalid",
                }
                for index, name in enumerate(module.PACKAGE_ORDER)
            ],
        }
        (root / "package-receipt.json").write_text(json.dumps(receipt), encoding="utf-8")
        result = subprocess.run(
            [str(SCRIPT), "--tag", "v1.2.3", "--packages", str(root)],
            cwd=ROOT,
            text=True,
            capture_output=True,
            check=False,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
        )
        assert result.returncode != 0 and "filename is not exact" in result.stderr


def main() -> int:
    test_security_helpers()
    test_tarball_manifest_and_receipt_filename()
    print("publish npm packages tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
