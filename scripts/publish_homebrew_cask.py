#!/usr/bin/env python3
"""Correct the just-published GoReleaser cask through GitHub's Contents API.

GoReleaser v2.17.1 has no cask template override for Homebrew's current
architecture stanza order. The release still uses GoReleaser's automatic tap
publication; this narrowly-scoped follow-up replaces only the exact generated
cask bytes with their structural normalization. It fails closed if the remote
file is not exactly the just-published output, making retries idempotent and
protecting a newer or manually changed tap revision.
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
            return json.load(response)
    except HTTPError as error:
        raise PublicationError(
            f"GitHub Contents API {method} failed with HTTP {error.code}"
        ) from None
    except URLError as error:
        raise PublicationError(f"GitHub Contents API {method} failed: {error.reason}") from None


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
    match = re.search(r'^  version "([^"]+)"$', cask, re.MULTILINE)
    if not match:
        raise PublicationError("generated cask has no version stanza")
    return match.group(1)


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
    normalized = normalize_cask_text(original)
    if normalized != original:
        dist_cask.write_text(normalized, encoding="utf-8", newline="")

    endpoint = (
        f"repos/{quote(owner, safe='')}/{quote(repository, safe='')}/contents/"
        f"{quote(path, safe='/')}?ref={quote(branch, safe='')}"
    )
    remote = _api_request(api_url, token, "GET", endpoint)
    remote_content = _decode_content(remote)
    remote_sha = remote.get("sha")
    if not isinstance(remote_sha, str) or not remote_sha:
        raise PublicationError("GitHub Contents API response has no file SHA")

    if remote_content == normalized:
        print("Homebrew cask publication: remote cask is already normalized")
        return False
    if remote_content != original:
        raise PublicationError(
            "remote cask differs from the just-published GoReleaser output; "
            "refusing to overwrite it"
        )

    version = _version(normalized)
    update = _api_request(
        api_url,
        token,
        "PUT",
        f"repos/{quote(owner, safe='')}/{quote(repository, safe='')}/contents/"
        f"{quote(path, safe='/')}",
        {
            "branch": branch,
            "content": base64.b64encode(normalized.encode("utf-8")).decode("ascii"),
            "message": f"chore: normalize Homebrew cask for {version}",
            "sha": remote_sha,
        },
    )
    updated_payload = update.get("content")
    if not isinstance(updated_payload, dict):
        raise PublicationError("GitHub Contents API update response has no file content")
    updated_content = _decode_content(updated_payload)
    if updated_content != normalized:
        raise PublicationError("GitHub Contents API returned bytes different from normalized cask")
    print("Homebrew cask publication: normalized and published cask")
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
