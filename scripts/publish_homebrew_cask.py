#!/usr/bin/env python3
"""Publish the normalized Homebrew cask through GitHub's Contents API.

GoReleaser v2.17.1 can generate the cask but cannot emit Homebrew's current
architecture stanza order.  GoReleaser therefore skips tap upload; this
publisher normalizes the local generated file and performs the one scoped
Contents API update.  It fails closed unless the existing tap file is either
the exact normalized bytes or a strict, older harness-lint-managed cask.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

from normalize_homebrew_cask import normalize_cask_text


class PublicationError(RuntimeError):
    """Raised when the tap cannot be safely updated."""


class _APIError(PublicationError):
    def __init__(self, method: str, status: int, message: str) -> None:
        super().__init__(message)
        self.method = method
        self.status = status


_SEMVER = re.compile(
    r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
    r"(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$"
)
_VERSION_LINE = re.compile(r'^  version "([^"]+)"$', re.MULTILINE)
_SHA_LINE = re.compile(r'^      sha256 "([0-9a-f]{64})"$', re.MULTILINE)
_URL_LINE = re.compile(r'^      url "([^"]+)"$', re.MULTILINE)
_REPOSITORY = "https://github.com/kespineira/harness-lint/releases/download"
_TARGETS = ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64")


def _api_request(
    api_url: str,
    token: str,
    method: str,
    endpoint: str,
    payload: dict[str, object] | None = None,
) -> dict[str, object]:
    body = None if payload is None else json.dumps(payload).encode("utf-8")
    request = Request(
        f"{api_url.rstrip('/')}/{endpoint.lstrip('/')}",
        data=body,
        method=method,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "User-Agent": "harness-lint-release-homebrew-cask",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urlopen(request, timeout=30) as response:
            payload = json.load(response)
    except HTTPError as error:
        raise _APIError(
            method,
            error.code,
            f"GitHub Contents API {method} failed with HTTP {error.code}",
        ) from None
    except URLError as error:
        raise PublicationError(f"GitHub Contents API {method} failed: {error.reason}") from None
    if not isinstance(payload, dict):
        raise PublicationError(f"GitHub Contents API {method} returned a non-object response")
    return payload


def _decode_content(payload: dict[str, object]) -> str:
    encoded = payload.get("content")
    if not isinstance(encoded, str):
        raise PublicationError("GitHub Contents API response has no file content")
    try:
        return base64.b64decode("".join(encoded.split()), validate=True).decode("utf-8")
    except (ValueError, UnicodeDecodeError) as error:
        raise PublicationError(
            f"GitHub Contents API returned invalid cask content: {error}"
        ) from None


def _version(cask: str) -> str:
    match = _VERSION_LINE.search(cask)
    if not match:
        raise PublicationError("cask has no version stanza")
    version = match.group(1)
    if _SEMVER.fullmatch(version) is None:
        raise PublicationError(f"cask has invalid semver {version!r}")
    return version


def _semver_key(version: str) -> tuple[int, int, int, tuple[tuple[int, object], ...]]:
    match = _SEMVER.fullmatch(version)
    if match is None:
        raise PublicationError(f"invalid semver {version!r}")
    prerelease = match.group(4)
    if prerelease is None:
        # The stable release sorts after every prerelease identifier.
        suffix: tuple[tuple[int, object], ...] = ((2, ""),)
    else:
        parts: list[tuple[int, object]] = []
        for item in prerelease.split("."):
            if item.isdigit():
                parts.append((0, int(item)))
            else:
                parts.append((1, item))
        suffix = tuple(parts)
    return (int(match.group(1)), int(match.group(2)), int(match.group(3)), suffix)


def _canonical_url(version: str, target: str) -> str:
    return f"{_REPOSITORY}/v{version}/harness-lint_#{{version}}_{target}.tar.gz"


def _managed_signature(cask: str) -> list[str]:
    """Return generated metadata with version, URLs, and checksums elided."""
    signature: list[str] = []
    for line in cask.splitlines():
        if _VERSION_LINE.fullmatch(line):
            signature.append('  version "<version>"')
        elif _SHA_LINE.fullmatch(line):
            signature.append('      sha256 "<sha256>"')
        elif _URL_LINE.fullmatch(line):
            signature.append('      url "<url>"')
        else:
            signature.append(line)
    return signature


def _validate_managed_cask(cask: str, expected_signature: list[str]) -> str:
    try:
        normalized = normalize_cask_text(cask)
    except ValueError as error:
        raise PublicationError(f"remote cask is not a generated harness-lint cask: {error}") from None
    version = _version(normalized)
    if _managed_signature(normalized) != expected_signature:
        raise PublicationError("remote cask is not a strict harness-lint-managed cask")
    if not normalized.startswith(
        '# This file was generated by GoReleaser. DO NOT EDIT.\n'
        'cask "harness-lint" do\n'
    ):
        raise PublicationError("remote cask is not a strict harness-lint-managed cask")

    urls = _URL_LINE.findall(normalized)
    if len(urls) != len(_TARGETS) or set(urls) != {
        _canonical_url(version, target) for target in _TARGETS
    }:
        raise PublicationError("remote cask does not use canonical harness-lint archive URLs")
    if len(_SHA_LINE.findall(normalized)) != len(_TARGETS):
        raise PublicationError("remote cask does not contain four platform checksums")
    return version


def _endpoint(owner: str, repository: str, path: str, branch: str | None = None) -> str:
    endpoint = (
        f"repos/{quote(owner, safe='')}/{quote(repository, safe='')}/contents/"
        f"{quote(path, safe='/')}"
    )
    if branch is not None:
        endpoint += f"?ref={quote(branch, safe='')}"
    return endpoint


def _read_remote(
    *,
    api_url: str,
    token: str,
    owner: str,
    repository: str,
    branch: str,
    path: str,
) -> tuple[str, str]:
    payload = _api_request(
        api_url,
        token,
        "GET",
        _endpoint(owner, repository, path, branch),
    )
    content = _decode_content(payload)
    sha = payload.get("sha")
    if not isinstance(sha, str) or not sha:
        raise PublicationError("GitHub Contents API response has no file SHA")
    return content, sha


def publish_cask(
    *,
    dist_cask: Path,
    owner: str,
    repository: str,
    branch: str,
    path: str,
    token: str,
    api_url: str,
) -> bool:
    try:
        original = dist_cask.read_text(encoding="utf-8")
    except OSError as error:
        raise PublicationError(f"cannot read generated cask: {error}") from None
    try:
        normalized = normalize_cask_text(original)
    except ValueError as error:
        raise PublicationError(f"generated cask cannot be normalized: {error}") from None
    if normalized != original:
        try:
            dist_cask.write_text(normalized, encoding="utf-8", newline="")
        except OSError as error:
            raise PublicationError(f"cannot write normalized cask: {error}") from None

    new_version = _version(normalized)
    _validate_managed_cask(normalized, _managed_signature(normalized))

    remote_content, remote_sha = _read_remote(
        api_url=api_url,
        token=token,
        owner=owner,
        repository=repository,
        branch=branch,
        path=path,
    )
    if remote_content == normalized:
        print("Homebrew cask publication: remote cask is already normalized")
        return False

    expected_signature = _managed_signature(normalized)
    remote_version = _validate_managed_cask(remote_content, expected_signature)
    if _semver_key(remote_version) >= _semver_key(new_version):
        if _semver_key(remote_version) == _semver_key(new_version):
            raise PublicationError("remote cask has the same version but different content")
        raise PublicationError("remote cask is newer than the generated cask")

    try:
        _api_request(
            api_url,
            token,
            "PUT",
            _endpoint(owner, repository, path),
            {
                "branch": branch,
                "content": base64.b64encode(normalized.encode("utf-8")).decode("ascii"),
                "message": f"chore: publish Homebrew cask for {new_version}",
                "sha": remote_sha,
            },
        )
    except _APIError as error:
        if error.method != "PUT" or error.status not in (409, 422):
            raise
        # A concurrent publisher may have won the SHA race. Reconcile only by
        # reading the file again; never retry the PUT against a new SHA.
        try:
            raced_content, _ = _read_remote(
                api_url=api_url,
                token=token,
                owner=owner,
                repository=repository,
                branch=branch,
                path=path,
            )
        except PublicationError as reconcile_error:
            raise PublicationError(
                "GitHub Contents API update conflicted and could not be reconciled safely"
            ) from reconcile_error
        if raced_content == normalized:
            print("Homebrew cask publication: concurrent update already published cask")
            return False
        raise PublicationError(
            "GitHub Contents API update conflicted; remote cask is not the generated cask"
        ) from None

    # Contents PUT responses contain commit/file metadata and are not required
    # to include file bytes. The fresh GET is the sole publication verification.
    verified_content, _ = _read_remote(
        api_url=api_url,
        token=token,
        owner=owner,
        repository=repository,
        branch=branch,
        path=path,
    )
    if verified_content != normalized:
        raise PublicationError("fresh Contents API read differs from normalized cask")
    print("Homebrew cask publication: published normalized cask")
    return True


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-cask", type=Path, required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--branch", default="main")
    parser.add_argument("--path", default="Casks/harness-lint.rb")
    parser.add_argument(
        "--api-url", default=os.environ.get("GITHUB_API_URL", "https://api.github.com")
    )
    args = parser.parse_args(argv)
    token = os.environ.get("HOMEBREW_TAP_TOKEN", "")
    if not token:
        print("Homebrew cask publication: HOMEBREW_TAP_TOKEN is required", file=sys.stderr)
        return 1
    try:
        publish_cask(
            dist_cask=args.dist_cask,
            owner=args.owner,
            repository=args.repository,
            branch=args.branch,
            path=args.path,
            token=token,
            api_url=args.api_url,
        )
    except PublicationError as error:
        print(f"Homebrew cask publication: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
