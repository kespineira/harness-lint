#!/usr/bin/env python3
"""Normalize the platform stanza layout emitted by GoReleaser's cask pipe.

GoReleaser v2.17.1 sorts platform packages lexically, which emits Intel before
Arm. Homebrew's current cask style requires Arm before Intel and no blank line
between the macOS and Linux system blocks. This module deliberately accepts
only the generated two-system, two-architecture shape and changes structure
only; all version, URL, and checksum values are preserved.
"""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import sys


class CaskNormalizationError(ValueError):
    """Raised when a cask is not the expected generated structure."""


_OS_START = re.compile(r"^  on_(macos|linux) do$")
_ARCH_START = re.compile(r"^    on_(arm|intel) do$")


def _find_end(lines: list[str], start: int, marker: str) -> int:
    for index in range(start + 1, len(lines)):
        if lines[index] == marker:
            return index
    raise CaskNormalizationError(
        f"missing {marker!r} for stanza starting at line {start + 1}"
    )


def _normalize_os_block(block: list[str], os_name: str) -> list[str]:
    starts = [
        (index, match.group(1))
        for index, line in enumerate(block)
        if (match := _ARCH_START.match(line))
    ]
    if len(starts) != 2 or {name for _, name in starts} != {"arm", "intel"}:
        raise CaskNormalizationError(
            f"{os_name} must contain exactly one on_arm and one on_intel stanza"
        )

    segments: dict[str, list[str]] = {}
    for position, (start, name) in enumerate(starts):
        end = _find_end(block, start, "    end")
        if position + 1 < len(starts) and end >= starts[position + 1][0]:
            raise CaskNormalizationError(f"overlapping {os_name} architecture stanzas")
        segments[name] = block[start : end + 1]

    first_start = starts[0][0]
    last_end = _find_end(block, starts[-1][0], "    end")
    if any(line.strip() for line in block[1:first_start]):
        raise CaskNormalizationError(f"unexpected content before {os_name} architecture stanzas")
    for position, (start, _) in enumerate(starts[:-1]):
        end = _find_end(block, start, "    end")
        next_start = starts[position + 1][0]
        if any(line.strip() for line in block[end + 1 : next_start]):
            raise CaskNormalizationError(
                f"unexpected content between {os_name} architecture stanzas"
            )
    if any(line.strip() for line in block[last_end + 1 : -1]):
        raise CaskNormalizationError(f"unexpected content after {os_name} architecture stanzas")

    return block[:first_start] + segments["arm"] + segments["intel"] + block[last_end + 1 :]


def normalize_cask_text(text: str) -> str:
    """Return a normalized generated cask, or raise on an unexpected shape."""

    had_newline = text.endswith(("\n", "\r"))
    lines = text.splitlines()
    starts = [
        (index, match.group(1))
        for index, line in enumerate(lines)
        if (match := _OS_START.match(line))
    ]
    if [name for _, name in starts] != ["macos", "linux"]:
        raise CaskNormalizationError(
            "generated cask must contain on_macos followed by on_linux"
        )

    blocks: list[list[str]] = []
    for position, (start, os_name) in enumerate(starts):
        end = _find_end(lines, start, "  end")
        if position + 1 < len(starts) and end >= starts[position + 1][0]:
            raise CaskNormalizationError("overlapping system stanzas")
        blocks.append(_normalize_os_block(lines[start : end + 1], os_name))

    first_start = starts[0][0]
    last_end = _find_end(lines, starts[-1][0], "  end")
    normalized = lines[:first_start] + blocks[0] + blocks[1] + lines[last_end + 1 :]
    result = "\n".join(normalized)
    return result + ("\n" if had_newline else "")


def normalize_cask_file(path: Path, *, write: bool) -> bool:
    original = path.read_text(encoding="utf-8")
    normalized = normalize_cask_text(original)
    changed = normalized != original
    if write and changed:
        path.write_text(normalized, encoding="utf-8", newline="")
    return changed


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("cask", type=Path)
    parser.add_argument(
        "--check",
        action="store_true",
        help="fail if normalization would change the cask",
    )
    args = parser.parse_args(argv)
    try:
        changed = normalize_cask_file(args.cask, write=not args.check)
    except (CaskNormalizationError, OSError) as error:
        print(f"Homebrew cask normalization: {error}", file=sys.stderr)
        return 1
    if args.check and changed:
        print(f"Homebrew cask normalization: {args.cask} needs normalization", file=sys.stderr)
        return 1
    state = "updated" if changed else "already normalized"
    print(f"Homebrew cask normalization: {state} {args.cask}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
