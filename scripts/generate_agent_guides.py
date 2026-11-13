#!/usr/bin/env python3
"""Generate tracked AGENTS.md files from their sibling CLAUDE.md guides."""

from __future__ import annotations

import argparse
import difflib
import subprocess
import sys
from pathlib import Path


SOURCE_NOTICE = (
    "> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). "
    "Edit this file, then run `make generate-agent-guides`; CI rejects drift."
)
GENERATED_NOTICE = (
    "> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). "
    "Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift."
)


def repo_root() -> Path:
    output = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True)
    return Path(output.strip()).resolve()


def tracked_files(root: Path) -> tuple[set[Path], set[Path]]:
    output = subprocess.check_output(["git", "ls-files", "-z"], cwd=root)
    return {
        Path(name.decode()).parent
        for name in output.split(b"\0")
        if name and Path(name.decode()).name == "CLAUDE.md"
    }, {
        Path(name.decode()).parent
        for name in output.split(b"\0")
        if name and Path(name.decode()).name == "AGENTS.md"
    }


def guide_directories(root: Path) -> list[Path]:
    claude_dirs, agent_dirs = tracked_files(root)
    if claude_dirs != agent_dirs:
        missing_agents = sorted(claude_dirs - agent_dirs)
        missing_sources = sorted(agent_dirs - claude_dirs)
        details = []
        if missing_agents:
            details.append("missing AGENTS.md: " + ", ".join(map(str, missing_agents)))
        if missing_sources:
            details.append("missing CLAUDE.md: " + ", ".join(map(str, missing_sources)))
        raise ValueError("unpaired agent guides; " + "; ".join(details))
    return [root / directory for directory in sorted(claude_dirs)]


def render_agents(source: str, path: Path) -> str:
    lines = source.splitlines(keepends=True)
    if len(lines) < 3 or not lines[0].rstrip().endswith(" — CLAUDE"):
        raise ValueError(f"{path}: first line must end with ' — CLAUDE'")
    if lines[2].rstrip() != SOURCE_NOTICE:
        raise ValueError(f"{path}: mirror notice does not match generator template")

    newline = "\n" if lines[0].endswith("\n") else ""
    lines[0] = lines[0].rstrip("\r\n")[: -len("CLAUDE")] + "AGENTS" + newline
    newline = "\n" if lines[2].endswith("\n") else ""
    lines[2] = GENERATED_NOTICE + newline
    return "".join(lines)


def run(check: bool) -> int:
    root = repo_root()
    directories = guide_directories(root)
    drifted = 0

    for directory in directories:
        source_path = directory / "CLAUDE.md"
        output_path = directory / "AGENTS.md"
        expected = render_agents(source_path.read_text(), source_path.relative_to(root))
        actual = output_path.read_text()
        if actual == expected:
            continue
        drifted += 1
        if check:
            sys.stdout.writelines(
                difflib.unified_diff(
                    actual.splitlines(keepends=True),
                    expected.splitlines(keepends=True),
                    fromfile=str(output_path.relative_to(root)),
                    tofile=f"{output_path.relative_to(root)} (generated)",
                )
            )
        else:
            output_path.write_text(expected)

    if check and drifted:
        print(
            f"{drifted} generated guide(s) are stale; "
            "run `make generate-agent-guides` and commit the result.",
            file=sys.stderr,
        )
        return 1

    action = "Checked" if check else "Generated"
    print(f"{action} {len(directories)} AGENTS.md mirrors.")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check", action="store_true", help="report drift without writing files"
    )
    args = parser.parse_args()
    try:
        return run(args.check)
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        print(f"agent guide generation failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
