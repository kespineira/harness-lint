#!/usr/bin/env python3
"""Publish audited npm tarballs with OIDC and verify every registry gate.

This command is intentionally resumable.  A name@version that already exists
is never republished: its registry metadata, tarball integrity, exact package
metadata, and npm CLI signature/provenance audit must match the audited local
tarball before the command continues to the next package.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import tarfile
import time
from typing import Any
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen


NATIVE_ORDER = [
    ("@kespineira/harness-lint-darwin-arm64", "darwin", "arm64"),
    ("@kespineira/harness-lint-darwin-x64", "darwin", "x64"),
    ("@kespineira/harness-lint-linux-arm64", "linux", "arm64"),
    ("@kespineira/harness-lint-linux-x64", "linux", "x64"),
]
PACKAGE_ORDER = [name for name, _, _ in NATIVE_ORDER] + ["harness-lint"]
REPOSITORY_URL = "https://github.com/kespineira/harness-lint"
VERSION_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
MAX_ATTEMPTS = 6
RETRY_DELAYS = (2, 4, 8, 16, 32)


class PublishError(Exception):
    pass


class RetryableProvenanceError(PublishError):
    """A registry propagation state that may become verifiable shortly."""


def fail(message: str) -> None:
    raise PublishError(message)


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read JSON {path}: {error}")


def clean_npm_environment(root: Path) -> dict[str, str]:
    """Isolate npm state while preserving ACTIONS_ID_TOKEN_REQUEST_* OIDC variables."""
    environment = {
        key: value
        for key, value in os.environ.items()
        if not key.lower().startswith("npm_config_")
        and key.upper() not in {"NPM_TOKEN", "NODE_AUTH_TOKEN"}
    }
    environment.update(
        {
            "HOME": str(root / "home"),
            "NPM_CONFIG_CACHE": str(root / "cache"),
            "NPM_CONFIG_USERCONFIG": str(root / "npmrc"),
            "NPM_CONFIG_GLOBALCONFIG": str(root / "global-npmrc"),
            "NPM_CONFIG_AUDIT": "false",
            "NPM_CONFIG_FUND": "false",
            "NPM_CONFIG_IGNORE_SCRIPTS": "true",
            "NPM_CONFIG_UPDATE_NOTIFIER": "false",
        }
    )
    for path in (root / "home", root / "cache"):
        path.mkdir()
    for path in (root / "npmrc", root / "global-npmrc"):
        path.write_text("", encoding="utf-8")
    return environment


def sha512_integrity(path: Path) -> str:
    digest = hashlib.sha512()
    try:
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
    except OSError as error:
        fail(f"cannot hash {path}: {error}")
    return "sha512-" + base64.b64encode(digest.digest()).decode("ascii")


def derive_version(tag: str) -> str:
    if not tag.startswith("v"):
        fail("release tag must begin with v")
    version = tag[1:]
    if not VERSION_RE.fullmatch(version):
        fail("release tag must be a stable vX.Y.Z version")
    return version


def package_url(registry: str, name: str) -> str:
    return registry.rstrip("/") + "/" + quote(name, safe="@")


def fetch_packument(registry: str, name: str) -> dict[str, Any] | None:
    request = Request(package_url(registry, name), headers={"Accept": "application/json"})
    try:
        with urlopen(request, timeout=30) as response:
            payload = json.loads(response.read().decode("utf-8"))
    except HTTPError as error:
        if error.code == 404:
            return None
        fail(f"registry metadata request for {name} failed with HTTP {error.code}")
    except (OSError, json.JSONDecodeError) as error:
        fail(f"registry metadata request for {name} failed: {error}")
    if not isinstance(payload, dict):
        fail(f"registry metadata for {name} is not an object")
    return payload


def attestation_endpoint_unavailable(detail: str) -> bool:
    """Recognize npm errors caused by an attestation endpoint not being ready."""
    normalized = detail.lower()
    mentions_attestation = "attest" in normalized or "provenance" in normalized
    propagation_markers = (
        "404",
        "e404",
        "not found",
        "not available",
        "unavailable",
        "not yet",
        "pending",
        "propagat",
        "timed out",
        "timeout",
        "eai_again",
        "enotfound",
    )
    return mentions_attestation and any(marker in normalized for marker in propagation_markers)


def _verify_audit_signatures_once(name: str, version: str, registry: str) -> None:
    """Use npm's documented consumer verification path for provenance."""
    with tempfile.TemporaryDirectory(prefix="harness-lint-npm-signatures-") as directory:
        root = Path(directory)
        (root / "package.json").write_text(
            json.dumps({"name": "harness-lint-signature-check", "version": "1.0.0", "private": True})
            + "\n",
            encoding="utf-8",
        )
        environment = clean_npm_environment(root)
        install = subprocess.run(
            [
                "npm",
                "install",
                "--force",
                "--ignore-scripts",
                "--no-audit",
                "--no-fund",
                "--no-update-notifier",
                "--package-lock=true",
                "--save-exact",
                "--registry",
                registry,
                f"{name}@{version}",
            ],
            cwd=root,
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
        if install.returncode != 0:
            detail = install.stderr.strip() or install.stdout.strip()
            raise RetryableProvenanceError(
                f"isolated npm install for provenance audit is not ready for {name}: {detail}"
            )
        audit = subprocess.run(
            [
                "npm",
                "audit",
                "signatures",
                "--json",
                "--include-attestations",
                "--registry",
                registry,
            ],
            cwd=root,
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
        detail = "\n".join(part for part in (audit.stderr.strip(), audit.stdout.strip()) if part)
        if audit.returncode != 0 and attestation_endpoint_unavailable(detail):
            raise RetryableProvenanceError(
                f"npm provenance attestation endpoint is not ready for {name}: {detail}"
            )
        try:
            report = json.loads(audit.stdout)
        except json.JSONDecodeError as error:
            fail(f"npm audit signatures returned invalid JSON for {name}: {error}")
        if not isinstance(report, dict):
            fail(f"npm provenance audit returned a non-object report for {name}")
        invalid = report.get("invalid")
        missing = report.get("missing")
        if not isinstance(invalid, list) or not isinstance(missing, list):
            fail(f"npm audit signatures omitted invalid/missing results for {name}")
        if invalid or missing:
            fail(f"npm audit signatures reported an invalid or missing record for {name}@{version}")
        verified = report.get("verified")
        if not isinstance(verified, list):
            fail(f"npm audit signatures omitted verified results for {name}")
        candidate = next(
            (
                item
                for item in verified
                if isinstance(item, dict)
                and item.get("name") == name
                and item.get("version") == version
            ),
            None,
        )
        if candidate is None:
            raise RetryableProvenanceError(
                f"npm audit signatures found no verified provenance attestation for {name}@{version}"
            )
        attestations = candidate.get("attestations")
        provenance = attestations.get("provenance") if isinstance(attestations, dict) else None
        predicate_type = provenance.get("predicateType") if isinstance(provenance, dict) else None
        if not isinstance(predicate_type, str) or not predicate_type.startswith("https://slsa.dev/provenance/"):
            fail(f"npm audit signatures found an invalid provenance attestation for {name}@{version}")
        if audit.returncode != 0:
            fail(f"npm provenance audit failed for {name}: {detail or 'unknown error'}")


def verify_audit_signatures(name: str, version: str, registry: str) -> None:
    """Verify provenance, retrying only bounded registry propagation states."""
    last_error: RetryableProvenanceError | None = None
    for attempt in range(MAX_ATTEMPTS):
        try:
            _verify_audit_signatures_once(name, version, registry)
            return
        except RetryableProvenanceError as error:
            last_error = error
            if attempt + 1 == MAX_ATTEMPTS:
                fail(f"{error} after bounded retry")
            time.sleep(RETRY_DELAYS[attempt])
    if last_error is not None:
        fail(f"{last_error} after bounded retry")


def repository_identity(value: Any) -> str | None:
    if isinstance(value, dict):
        value = value.get("url")
    if not isinstance(value, str):
        return None
    normalized = value.strip()
    if normalized.startswith("git+"):
        normalized = normalized[4:]
    return normalized.removesuffix(".git").removesuffix("/").lower()


def manifest_from_tarball(path: Path, expected_name: str, version: str) -> dict[str, Any]:
    try:
        with tarfile.open(path, "r:gz") as archive:
            member = archive.getmember("package/package.json")
            if not member.isreg():
                fail(f"package manifest is not a regular file in {path.name}")
            stream = archive.extractfile(member)
            if stream is None:
                fail(f"cannot read package manifest in {path.name}")
            manifest = json.loads(stream.read().decode("utf-8"))
    except (OSError, tarfile.TarError, json.JSONDecodeError) as error:
        fail(f"cannot inspect package manifest in {path.name}: {error}")
    if not isinstance(manifest, dict) or manifest.get("name") != expected_name or manifest.get("version") != version:
        fail(f"audited package manifest mismatch for {expected_name}@{version}")
    if repository_identity(manifest.get("repository")) != repository_identity(REPOSITORY_URL):
        fail(f"audited package repository metadata mismatch for {expected_name}@{version}")
    scripts = manifest.get("scripts")
    if scripts:
        fail(f"audited package contains lifecycle scripts: {expected_name}")
    if expected_name == "harness-lint":
        if manifest.get("optionalDependencies") != {item[0]: version for item in NATIVE_ORDER}:
            fail("audited root optionalDependencies do not exactly match the release")
    else:
        expected = next(item for item in NATIVE_ORDER if item[0] == expected_name)
        if manifest.get("os") != [expected[1]] or manifest.get("cpu") != [expected[2]]:
            fail(f"audited native platform metadata mismatch for {expected_name}")
    return manifest


def verify_registry(registry: str, name: str, version: str, local: dict[str, Any]) -> None:
    """Verify exact registry state, then verify provenance with npm CLI."""
    packument: dict[str, Any] | None = None
    for attempt in range(MAX_ATTEMPTS):
        packument = fetch_packument(registry, name)
        version_record = packument.get("versions", {}).get(version) if packument else None
        if isinstance(version_record, dict):
            break
        if attempt + 1 < MAX_ATTEMPTS:
            time.sleep(RETRY_DELAYS[attempt])
    if not isinstance(packument, dict):
        fail(f"registry has no metadata for {name}@{version}")
    versions = packument.get("versions")
    record = versions.get(version) if isinstance(versions, dict) else None
    if not isinstance(record, dict):
        fail(f"registry did not expose exact version {name}@{version} after bounded retry")
    if packument.get("name") != name or record.get("name") != name or record.get("version") != version:
        fail(f"registry name/version metadata mismatch for {name}@{version}")
    if packument.get("dist-tags", {}).get("latest") != version:
        fail(f"registry latest dist-tag is not {version} for {name}")
    dist = record.get("dist")
    if not isinstance(dist, dict) or dist.get("integrity") != local["integrity"]:
        fail(f"registry tarball integrity does not match audited tarball for {name}@{version}")
    if repository_identity(record.get("repository")) != repository_identity(REPOSITORY_URL):
        fail(f"registry repository metadata mismatch for {name}@{version}")
    if name == "harness-lint":
        if record.get("optionalDependencies") != {item[0]: version for item in NATIVE_ORDER}:
            fail("registry root optionalDependencies do not exactly match the release")
    else:
        expected = next(item for item in NATIVE_ORDER if item[0] == name)
        if record.get("os") != [expected[1]] or record.get("cpu") != [expected[2]]:
            fail(f"registry platform metadata mismatch for {name}@{version}")
    verify_audit_signatures(name, version, registry)


def publish_one(registry: str, name: str, version: str, tarball: Path, local: dict[str, Any]) -> None:
    before = fetch_packument(registry, name)
    existing = before.get("versions", {}).get(version) if before else None
    if isinstance(existing, dict):
        verify_registry(registry, name, version, local)
        print(f"verified existing immutable npm package {name}@{version}")
        return
    for attempt in range(MAX_ATTEMPTS):
        with tempfile.TemporaryDirectory(prefix="harness-lint-npm-publish-") as directory:
            result = subprocess.run(
                [
                    "npm",
                    "publish",
                    str(tarball),
                    "--access",
                    "public",
                    "--tag",
                    "latest",
                    "--registry",
                    registry,
                    "--provenance",
                    "--ignore-scripts",
                ],
                cwd=Path(directory),
                text=True,
                capture_output=True,
                check=False,
                env=clean_npm_environment(Path(directory)),
            )
        if result.returncode == 0:
            break
        observed = fetch_packument(registry, name)
        observed_version = observed.get("versions", {}).get(version) if observed else None
        if isinstance(observed_version, dict):
            break
        if attempt + 1 == MAX_ATTEMPTS:
            detail = result.stderr.strip() or result.stdout.strip()
            fail(f"npm publish failed for {name}@{version} after bounded retry: {detail}")
        time.sleep(RETRY_DELAYS[attempt])
    verify_registry(registry, name, version, local)
    print(f"published and verified npm package {name}@{version}")


def publish(args: argparse.Namespace) -> None:
    if "NPM_TOKEN" in os.environ or "NODE_AUTH_TOKEN" in os.environ:
        fail("normal npm publication must not have NPM_TOKEN or NODE_AUTH_TOKEN")
    version = args.version or derive_version(args.tag or os.environ.get("GITHUB_REF_NAME", ""))
    if not VERSION_RE.fullmatch(version):
        fail("npm version must be a stable semver without a leading v")
    packages_dir = Path(args.packages).expanduser().resolve()
    receipt = read_json(packages_dir / "package-receipt.json")
    if not isinstance(receipt, dict) or receipt.get("schemaVersion") != 1:
        fail("package receipt must be schemaVersion 1")
    if receipt.get("version") != version or receipt.get("releaseVersion") != version or receipt.get("stable") is not True:
        fail("package receipt is not the validated stable tag version")
    packages = receipt.get("packages")
    if not isinstance(packages, list) or [item.get("name") for item in packages if isinstance(item, dict)] != PACKAGE_ORDER:
        fail("package receipt order must be native packages followed by root")
    by_name = {item["name"]: item for item in packages}
    for name in PACKAGE_ORDER:
        item = by_name[name]
        expected_filename = f"{name.replace('@', '').replace('/', '-')}-{version}.tgz"
        filename = item.get("filename")
        if not isinstance(filename, str) or Path(filename).name != filename or filename != expected_filename:
            fail(f"package receipt filename is not exact for {name}")
        tarball = packages_dir / filename
        try:
            tarball.lstat()
        except OSError as error:
            fail(f"audited tarball is missing for {name}: {error}")
        if not tarball.is_file() or tarball.is_symlink():
            fail(f"audited tarball is not a regular non-symlink file for {name}")
        actual_sha256 = hashlib.sha256(tarball.read_bytes()).hexdigest()
        if item.get("sha256") != actual_sha256:
            fail(f"audited tarball SHA-256 receipt mismatch for {name}")
        actual_integrity = sha512_integrity(tarball)
        if item.get("integrity") != actual_integrity:
            fail(f"audited tarball integrity receipt mismatch for {name}")
        manifest_from_tarball(tarball, name, version)
        publish_one(args.registry, name, version, tarball, item)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--packages", required=True, help="audited npm tarball directory")
    result.add_argument("--version", help="validated semver without v (defaults to tag-derived version)")
    result.add_argument("--tag", help="validated vX.Y.Z tag (used to derive version)")
    result.add_argument("--registry", default="https://registry.npmjs.org", help="npm registry URL")
    return result


def main() -> int:
    try:
        publish(parser().parse_args())
        print("all five npm packages published and provenance-verified")
        return 0
    except PublishError as error:
        print(f"publish npm packages: {error}", file=sys.stderr)
        return 1
    except (OSError, KeyError, TypeError, ValueError) as error:
        print(f"publish npm packages: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
