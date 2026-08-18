#!/usr/bin/env python3
"""Tests for the fail-closed Homebrew Contents API publisher."""

from __future__ import annotations

from pathlib import Path
import tempfile

import publish_homebrew_cask as publisher
from normalize_homebrew_cask_test import SAMPLE
from normalize_homebrew_cask import normalize_cask_text


def main() -> int:
    normalized = normalize_cask_text(SAMPLE)
    with tempfile.TemporaryDirectory() as temporary:
        cask = Path(temporary) / "harness-lint.rb"
        cask.write_text(SAMPLE, encoding="utf-8")
        calls: list[tuple[str, str, dict[str, object] | None]] = []

        def fake_request(api_url: str, token: str, method: str, endpoint: str, payload=None):
            del api_url, token
            calls.append((method, endpoint, payload))
            if method == "GET":
                return {
                    "content": publisher.base64.b64encode(SAMPLE.encode()).decode(),
                    "sha": "old-sha",
                }
            return {
                "content": {
                    "content": publisher.base64.b64encode(normalized.encode()).decode()
                }
            }

        original_request = publisher._api_request
        publisher._api_request = fake_request
        try:
            assert publisher.publish_cask(
                dist_cask=cask,
                owner="owner",
                repository="homebrew-tap",
                branch="main",
                path="Casks/harness-lint.rb",
                token="test-token",
                api_url="https://api.example.invalid",
            )
        finally:
            publisher._api_request = original_request
        assert cask.read_text(encoding="utf-8") == normalized
        assert [method for method, _, _ in calls] == ["GET", "PUT"]
        assert calls[1][2]["sha"] == "old-sha"
        assert calls[1][2]["branch"] == "main"
        assert calls[1][2]["content"] == publisher.base64.b64encode(normalized.encode()).decode()

        def already_normalized(api_url: str, token: str, method: str, endpoint: str, payload=None):
            del api_url, token, method, endpoint, payload
            return {
                "content": publisher.base64.b64encode(normalized.encode()).decode(),
                "sha": "new-sha",
            }

        publisher._api_request = already_normalized
        try:
            assert not publisher.publish_cask(
                dist_cask=cask,
                owner="owner",
                repository="homebrew-tap",
                branch="main",
                path="Casks/harness-lint.rb",
                token="test-token",
                api_url="https://api.example.invalid",
            )
        finally:
            publisher._api_request = original_request

        def unexpected_remote(api_url: str, token: str, method: str, endpoint: str, payload=None):
            del api_url, token, method, endpoint, payload
            return {
                "content": publisher.base64.b64encode(b"# unexpected tap edit\n").decode(),
                "sha": "unexpected-sha",
            }

        publisher._api_request = unexpected_remote
        try:
            try:
                publisher.publish_cask(
                    dist_cask=cask,
                    owner="owner",
                    repository="homebrew-tap",
                    branch="main",
                    path="Casks/harness-lint.rb",
                    token="test-token",
                    api_url="https://api.example.invalid",
                )
            except publisher.PublicationError:
                pass
            else:
                raise AssertionError("unexpected remote cask was overwritten")
        finally:
            publisher._api_request = original_request

    print("Homebrew cask publisher tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
