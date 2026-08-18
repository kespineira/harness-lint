#!/usr/bin/env python3
"""Tests for the fail-closed Homebrew Contents API publisher."""

from __future__ import annotations

from pathlib import Path
import re
import tempfile

import publish_homebrew_cask as publisher
from normalize_homebrew_cask import normalize_cask_text
from normalize_homebrew_cask_test import SAMPLE


def _cask(version: str, seed: int = 1) -> str:
    text = SAMPLE.replace("1.2.3", version)
    checksums = iter(str((seed + index) % 10) * 64 for index in range(4))
    return re.sub(r'(?m)^      sha256 "[^"]+"$', lambda match: f'      sha256 "{next(checksums)}"', text)


def _args(cask: Path) -> dict[str, object]:
    return {
        "dist_cask": cask,
        "owner": "owner",
        "repository": "homebrew-tap",
        "branch": "main",
        "path": "Casks/harness-lint.rb",
        "token": "test-token",
        "api_url": "https://api.example.invalid",
    }


def _response(content: str, sha: str) -> dict[str, object]:
    return {
        "content": publisher.base64.b64encode(content.encode()).decode(),
        "sha": sha,
    }


def _run_with_responses(cask: Path, responses: list[dict[str, object]]) -> tuple[bool, list[str]]:
    calls: list[str] = []
    original_request = publisher._api_request
    queue = iter(responses)

    def fake_request(api_url: str, token: str, method: str, endpoint: str, payload=None):
        del api_url, token, endpoint, payload
        calls.append(method)
        return next(queue)

    publisher._api_request = fake_request
    try:
        result = publisher.publish_cask(**_args(cask))
    finally:
        publisher._api_request = original_request
    return result, calls


def _assert_publication_error(cask: Path, responses: list[dict[str, object]], expected: str) -> None:
    try:
        _run_with_responses(cask, responses)
    except publisher.PublicationError as error:
        assert expected in str(error), str(error)
    else:
        raise AssertionError(f"expected PublicationError containing {expected!r}")


def _run_conflict(cask: Path, raced: str) -> tuple[bool, list[str]]:
    calls: list[str] = []
    original_request = publisher._api_request
    old = normalize_cask_text(_cask("1.2.2"))
    responses = iter([_response(old, "old-sha"), _response(raced, "raced-sha")])

    def fake_request(api_url: str, token: str, method: str, endpoint: str, payload=None):
        del api_url, token, endpoint, payload
        calls.append(method)
        if method == "PUT":
            raise publisher._APIError("PUT", 409, "conflict")
        return next(responses)

    publisher._api_request = fake_request
    try:
        result = publisher.publish_cask(**_args(cask))
    finally:
        publisher._api_request = original_request
    return result, calls


def main() -> int:
    new = _cask("1.2.3")
    old = normalize_cask_text(_cask("1.2.2"))
    normalized_new = normalize_cask_text(new)

    with tempfile.TemporaryDirectory() as temporary:
        cask = Path(temporary) / "harness-lint.rb"

        # An older managed cask is replaced once. A Contents PUT response may
        # contain metadata only; the fresh GET is the verification source.
        cask.write_text(new, encoding="utf-8")
        result, calls = _run_with_responses(
            cask,
            [_response(old, "old-sha"), {"commit": {"sha": "commit-sha"}}, _response(normalized_new, "new-sha")],
        )
        assert result
        assert calls == ["GET", "PUT", "GET"]
        assert cask.read_text(encoding="utf-8") == normalized_new

        # Rerunning against exact normalized bytes is idempotent and does not PUT.
        cask.write_text(normalized_new, encoding="utf-8")
        result, calls = _run_with_responses(cask, [_response(normalized_new, "new-sha")])
        assert not result
        assert calls == ["GET"]

        # Unrecognized/manual content is never overwritten.
        cask.write_text(new, encoding="utf-8")
        _assert_publication_error(
            cask,
            [_response("# manual tap edit\n", "manual-sha")],
            "generated harness-lint cask",
        )

        # Newer managed content is protected.
        _assert_publication_error(
            cask,
            [_response(normalize_cask_text(_cask("1.2.4")), "newer-sha")],
            "newer",
        )

        # Same-version content with a different checksum is not idempotent.
        same_version_different = _cask("1.2.3", seed=7)
        _assert_publication_error(
            cask,
            [_response(normalize_cask_text(same_version_different), "same-sha")],
            "same version",
        )

        # A successful PUT followed by a mismatching read fails closed.
        _assert_publication_error(
            cask,
            [_response(old, "old-sha"), {"commit": {"sha": "commit-sha"}}, _response(old, "raced-sha")],
            "fresh Contents API read differs",
        )

        # A SHA race is reconciled with one fresh read and never a second PUT.
        result, calls = _run_conflict(cask, normalized_new)
        assert not result
        assert calls == ["GET", "PUT", "GET"]
        try:
            _run_conflict(cask, old)
        except publisher.PublicationError as error:
            assert "conflicted" in str(error)
        else:
            raise AssertionError("conflict with different bytes was accepted")

    print("Homebrew cask publisher tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
