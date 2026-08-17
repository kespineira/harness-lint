#!/usr/bin/env python3
"""Focused deterministic tests for the resumable npm publisher."""

from __future__ import annotations

import importlib.util
import hashlib
import io
import json
from pathlib import Path
import os
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
from unittest import mock


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
        injected = {
            "NPM_TOKEN": "must-not-leak",
            "node_auth_token": "must-not-leak",
            "nPm_CoNfIg_UserConfig": "must-not-leak",
            "NPM_CONFIG_CACHE": "must-not-leak",
            "ACTIONS_ID_TOKEN_REQUEST_URL": "https://actions.example/oidc",
        }
        old_values = {key: os.environ.get(key) for key in injected}
        os.environ.update(injected)
        try:
            environment = module.clean_npm_environment(Path(directory))
        finally:
            for key, old_value in old_values.items():
                if old_value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = old_value
        assert "NPM_TOKEN" not in environment
        assert "node_auth_token" not in environment
        assert "nPm_CoNfIg_UserConfig" not in environment
        assert "NPM_CONFIG_CACHE" not in environment or environment["NPM_CONFIG_CACHE"].endswith("cache")
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


def write_tarball(root: Path, name: str, version: str = "1.2.3") -> tuple[Path, dict]:
    native = name != "harness-lint"
    manifest = {
        "name": name,
        "version": version,
        "repository": {"type": "git", "url": "git+https://github.com/kespineira/harness-lint.git"},
    }
    if native:
        expected = next(item for item in module.NATIVE_ORDER if item[0] == name)
        manifest.update({"os": [expected[1]], "cpu": [expected[2]]})
    else:
        manifest["optionalDependencies"] = {item[0]: version for item in module.NATIVE_ORDER}
    filename = f"{name.replace('@', '').replace('/', '-')}-{version}.tgz"
    path = root / filename
    files = {"package/package.json": json.dumps(manifest).encode(), "package/README.md": b"readme\n", "package/LICENSE": b"license\n"}
    files["package/bin/harness-lint" if native else "package/bin/harness-lint.js"] = b"#!/bin/sh\n"
    with tarfile.open(path, "w:gz") as archive:
        for member_name, payload in files.items():
            info = tarfile.TarInfo(member_name)
            info.size = len(payload)
            info.mode = 0o755 if "/bin/" in member_name else 0o644
            archive.addfile(info, io.BytesIO(payload))
    return path, manifest


def package_fixture(root: Path) -> tuple[dict, dict[str, dict]]:
    records = []
    manifests = {}
    for name in module.PACKAGE_ORDER:
        path, manifest = write_tarball(root, name)
        manifests[name] = manifest
        records.append(
            {
                "name": name,
                "filename": path.name,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                "integrity": module.sha512_integrity(path),
            }
        )
    receipt = {"schemaVersion": 1, "version": "1.2.3", "releaseVersion": "1.2.3", "stable": True, "packages": records}
    (root / "package-receipt.json").write_text(json.dumps(receipt), encoding="utf-8")
    return receipt, manifests


def packument(name: str, record: dict, manifest: dict) -> dict:
    metadata = {
        "name": name,
        "dist-tags": {"latest": "1.2.3"},
        "versions": {
            "1.2.3": {
                **manifest,
                "dist": {"integrity": record["integrity"]},
            }
        },
    }
    return metadata


def publish_args(root: Path) -> SimpleNamespace:
    return SimpleNamespace(packages=str(root), tag="v1.2.3", version=None, registry="https://registry.npmjs.org")


def test_native_order_resume_and_root_gate() -> None:
    with tempfile.TemporaryDirectory(prefix="harness-lint-publish-order-") as directory:
        root = Path(directory)
        receipt, manifests = package_fixture(root)
        records = {item["name"]: item for item in receipt["packages"]}
        state = {
            module.NATIVE_ORDER[0][0]: packument(module.NATIVE_ORDER[0][0], records[module.NATIVE_ORDER[0][0]], manifests[module.NATIVE_ORDER[0][0]])
        }
        events: list[tuple[str, str]] = []

        def fake_fetch(_registry: str, name: str):
            return state.get(name)

        def fake_audit(name: str, _version: str, _registry: str):
            events.append(("verify", name))

        def fake_run(command, **_kwargs):
            if command[1] == "publish":
                tarball = Path(command[2])
                name = next(item for item in module.PACKAGE_ORDER if item.replace("@", "").replace("/", "-") in tarball.name)
                state[name] = packument(name, records[name], manifests[name])
                events.append(("publish", name))
            return subprocess.CompletedProcess(command, 0, "", "")

        with mock.patch.object(module, "fetch_packument", fake_fetch), mock.patch.object(module, "verify_audit_signatures", fake_audit), mock.patch.object(module.subprocess, "run", fake_run):
            module.publish(publish_args(root))
        assert [name for action, name in events if action == "publish"] == module.PACKAGE_ORDER[1:]
        root_publish = events.index(("publish", "harness-lint"))
        assert all(events.index(("verify", name)) < root_publish for name, _, _ in module.NATIVE_ORDER)
        assert events[0] == ("verify", module.NATIVE_ORDER[0][0])


def test_existing_mismatch_fails_closed_and_receipts_are_bound() -> None:
    for field, expected_message in (("sha256", "SHA-256"), ("integrity", "integrity")):
        with tempfile.TemporaryDirectory(prefix="harness-lint-publish-receipt-") as directory:
            root = Path(directory)
            receipt, _ = package_fixture(root)
            receipt["packages"][0][field] = "0" * (64 if field == "sha256" else 10)
            (root / "package-receipt.json").write_text(json.dumps(receipt), encoding="utf-8")
            with mock.patch.object(module, "fetch_packument", lambda *_args: None):
                try:
                    module.publish(publish_args(root))
                except module.PublishError as error:
                    assert expected_message in str(error)
                else:
                    raise AssertionError(f"accepted bad {field} receipt")
    with tempfile.TemporaryDirectory(prefix="harness-lint-publish-mismatch-") as directory:
        root = Path(directory)
        receipt, manifests = package_fixture(root)
        name = module.NATIVE_ORDER[0][0]
        bad = packument(name, receipt["packages"][0], manifests[name])
        bad["versions"]["1.2.3"]["dist"]["integrity"] = "sha512-wrong"
        with mock.patch.object(module, "fetch_packument", lambda _registry, _name: bad), mock.patch.object(module, "verify_audit_signatures"):
            try:
                module.publish(publish_args(root))
            except module.PublishError as error:
                assert "integrity" in str(error)
            else:
                raise AssertionError("accepted mismatched existing registry state")
    with tempfile.TemporaryDirectory(prefix="harness-lint-publish-manifest-") as directory:
        root = Path(directory)
        receipt, _ = package_fixture(root)
        target = root / receipt["packages"][0]["filename"]
        rewritten = root / "rewritten.tgz"
        with tarfile.open(target, "r:gz") as source, tarfile.open(rewritten, "w:gz") as destination:
            for member in source.getmembers():
                payload = source.extractfile(member).read() if member.isreg() else None
                if member.name == "package/package.json":
                    changed = json.loads(payload.decode())
                    changed["version"] = "9.9.9"
                    payload = json.dumps(changed).encode()
                    member.size = len(payload)
                destination.addfile(member, io.BytesIO(payload) if payload is not None else None)
        rewritten.replace(target)
        receipt["packages"][0]["sha256"] = hashlib.sha256(target.read_bytes()).hexdigest()
        receipt["packages"][0]["integrity"] = module.sha512_integrity(target)
        (root / "package-receipt.json").write_text(json.dumps(receipt), encoding="utf-8")
        with mock.patch.object(module, "fetch_packument", lambda *_args: None):
            try:
                module.publish(publish_args(root))
            except module.PublishError as error:
                assert "manifest mismatch" in str(error)
            else:
                raise AssertionError("accepted a tarball with a mismatched manifest")


def test_audit_rejects_missing_and_invalid_records() -> None:
    target = {"name": "pkg", "version": "1.0.0"}
    reports = [
        {"verified": [], "missing": [target], "invalid": []},
        {"verified": [], "missing": [], "invalid": [target]},
    ]
    for report in reports:
        def fake_run(command, **_kwargs):
            output = json.dumps(report) if command[1] == "audit" else ""
            return subprocess.CompletedProcess(command, 0, output, "")

        with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
            try:
                module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
            except module.PublishError:
                assert not sleep.called
            else:
                raise AssertionError(f"accepted bad npm audit report: {report}")


def test_audit_retries_missing_provenance_then_succeeds() -> None:
    target = {"name": "pkg", "version": "1.0.0"}
    verified = {
        **target,
        "attestations": {
            "provenance": {"predicateType": "https://slsa.dev/provenance/v1"}
        },
    }
    reports = [
        {"verified": [{**target}], "missing": [], "invalid": []},
        {"verified": [{**target, "attestations": {}}], "missing": [], "invalid": []},
        {"verified": [{**target, "attestations": {"provenance": {}}}], "missing": [], "invalid": []},
        {"verified": [verified], "missing": [], "invalid": []},
    ]
    calls = 0

    def fake_run(command, **_kwargs):
        nonlocal calls
        if command[1] == "audit":
            report = reports[min(calls, len(reports) - 1)]
            calls += 1
            return subprocess.CompletedProcess(command, 0, json.dumps(report), "")
        return subprocess.CompletedProcess(command, 0, "", "")

    with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
        module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
    assert calls == 4
    assert [call.args[0] for call in sleep.call_args_list] == [2, 4, 8]


def test_audit_missing_provenance_is_bounded() -> None:
    target = {"name": "pkg", "version": "1.0.0"}
    report = {"verified": [{**target, "attestations": {"provenance": {}}}], "missing": [], "invalid": []}
    calls = 0

    def fake_run(command, **_kwargs):
        nonlocal calls
        if command[1] == "audit":
            calls += 1
            return subprocess.CompletedProcess(command, 0, json.dumps(report), "")
        return subprocess.CompletedProcess(command, 0, "", "")

    with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
        try:
            module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
        except module.PublishError as error:
            assert "after bounded retry" in str(error)
        else:
            raise AssertionError("accepted an exact package without propagated provenance")
    assert calls == module.MAX_ATTEMPTS
    assert [call.args[0] for call in sleep.call_args_list] == list(module.RETRY_DELAYS)


def test_audit_rejects_invalid_predicate_immediately() -> None:
    target = {
        "name": "pkg",
        "version": "1.0.0",
        "attestations": {"provenance": {"predicateType": "https://example.invalid/provenance/v1"}},
    }
    report = {"verified": [target], "missing": [], "invalid": []}

    def fake_run(command, **_kwargs):
        output = json.dumps(report) if command[1] == "audit" else ""
        return subprocess.CompletedProcess(command, 0, output, "")

    with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
        try:
            module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
        except module.PublishError as error:
            assert "invalid provenance attestation" in str(error)
        else:
            raise AssertionError("accepted non-SLSA provenance predicate")
    assert not sleep.called


def test_audit_retries_propagation_then_succeeds() -> None:
    target = {"name": "pkg", "version": "1.0.0"}
    verified = {
        **target,
        "attestations": {
            "provenance": {"predicateType": "https://slsa.dev/provenance/v1"}
        },
    }
    audit_calls = 0
    install_calls = 0

    def fake_run(command, **_kwargs):
        nonlocal audit_calls, install_calls
        if command[1] == "install":
            install_calls += 1
            if install_calls == 1:
                return subprocess.CompletedProcess(command, 1, "", "package is not resolved yet")
            return subprocess.CompletedProcess(command, 0, "", "")
        audit_calls += 1
        if audit_calls == 1:
            return subprocess.CompletedProcess(command, 1, "", "attestation endpoint not found yet")
        report = {"verified": [] if audit_calls == 2 else [verified], "missing": [], "invalid": []}
        return subprocess.CompletedProcess(command, 0, json.dumps(report), "")

    with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
        module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
    assert install_calls == 4
    assert audit_calls == 3
    assert [call.args[0] for call in sleep.call_args_list] == [2, 4, 8]


def test_audit_propagation_retry_is_bounded() -> None:
    report = {"verified": [], "missing": [], "invalid": []}
    calls = 0

    def fake_run(command, **_kwargs):
        nonlocal calls
        if command[1] == "audit":
            calls += 1
            return subprocess.CompletedProcess(command, 0, json.dumps(report), "")
        return subprocess.CompletedProcess(command, 0, "", "")

    with mock.patch.object(module.subprocess, "run", fake_run), mock.patch.object(module.time, "sleep") as sleep:
        try:
            module.verify_audit_signatures("pkg", "1.0.0", "https://registry.npmjs.org")
        except module.PublishError as error:
            assert "after bounded retry" in str(error)
        else:
            raise AssertionError("accepted an unattested package after bounded retry")
    assert calls == module.MAX_ATTEMPTS
    assert [call.args[0] for call in sleep.call_args_list] == list(module.RETRY_DELAYS)


def main() -> int:
    test_security_helpers()
    test_tarball_manifest_and_receipt_filename()
    test_native_order_resume_and_root_gate()
    test_existing_mismatch_fails_closed_and_receipts_are_bound()
    test_audit_rejects_missing_and_invalid_records()
    test_audit_retries_missing_provenance_then_succeeds()
    test_audit_missing_provenance_is_bounded()
    test_audit_rejects_invalid_predicate_immediately()
    test_audit_retries_propagation_then_succeeds()
    test_audit_propagation_retry_is_bounded()
    print("publish npm packages tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
