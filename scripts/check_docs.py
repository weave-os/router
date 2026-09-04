#!/usr/bin/env python3
"""Check repository-local documentation links, symbols, and index drift."""

from __future__ import annotations

import html
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path
from urllib.parse import unquote, urlsplit


INLINE_LINK_RE = re.compile(r"!?\[[^\]]*\]\((<[^>]+>|[^)\s]+)")
REFERENCE_LINK_RE = re.compile(r"^\s*\[(?!\^)[^\]]+\]:\s*(<[^>]+>|\S+)", re.MULTILINE)
HEADING_RE = re.compile(r"^ {0,3}#{1,6}\s+(.+?)\s*#*\s*$", re.MULTILINE)
CODE_SPAN_RE = re.compile(r"(?<!`)`([^`\n]+)`(?!`)")
QUALIFIED_SYMBOL_RE = re.compile(r"\b([a-z][a-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)\b")
PACKAGE_RE = re.compile(r"^package\s+(\w+)", re.MULTILINE)
FENCE_RE = re.compile(r"^ {0,3}(```|~~~).*?^ {0,3}\1\s*$", re.MULTILINE | re.DOTALL)

# These public governance documents are maintained by a separate workstream.
# Keeping the exceptions exact prevents them from masking any other broken link.
KNOWN_MISSING_LINKS = {
    (Path("CONTRIBUTING.md"), "CODE_OF_CONDUCT.md"),
    (Path("CONTRIBUTING.md"), "SECURITY.md"),
}


def repo_root() -> Path:
    output = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True)
    return Path(output.strip()).resolve()


def tracked(root: Path, pattern: str) -> list[Path]:
    output = subprocess.check_output(["git", "ls-files", "-z", "--", pattern], cwd=root)
    return [Path(name.decode()) for name in output.split(b"\0") if name]


def without_fences(text: str) -> str:
    """Mask fenced blocks while preserving every source character offset."""
    return FENCE_RE.sub(
        lambda match: "".join("\n" if char == "\n" else " " for char in match.group(0)),
        text,
    )


def line_number(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def markdown_targets(text: str):
    for pattern in (INLINE_LINK_RE, REFERENCE_LINK_RE):
        for match in pattern.finditer(without_fences(text)):
            yield match.start(), match.group(1).strip("<>")


def github_slug(value: str) -> str:
    value = re.sub(r"!\[([^\]]*)\]\([^)]*\)", r"\1", value)
    value = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", value)
    value = re.sub(r"<[^>]+>", "", value)
    value = html.unescape(value).lower().replace("`", "")
    value = "".join(char for char in value if char.isalnum() or char in {" ", "-", "_"})
    return re.sub(r"\s", "-", value)


def anchors(path: Path) -> set[str]:
    seen: defaultdict[str, int] = defaultdict(int)
    result = set()
    text = without_fences(path.read_text())
    for match in HEADING_RE.finditer(text):
        base = github_slug(match.group(1))
        count = seen[base]
        seen[base] += 1
        result.add(base if count == 0 else f"{base}-{count}")
    result.update(re.findall(r"\bid=[\"']([^\"']+)[\"']", text))
    return result


def check_links(
    root: Path, markdown: list[Path]
) -> tuple[list[str], dict[Path, set[Path]]]:
    errors = []
    linked: dict[Path, set[Path]] = defaultdict(set)
    anchor_cache: dict[Path, set[str]] = {}

    for relative in markdown:
        path = root / relative
        text = path.read_text()
        for offset, raw_target in markdown_targets(text):
            parsed = urlsplit(raw_target)
            if parsed.scheme or parsed.netloc or parsed.path.startswith("/"):
                continue
            target_text = unquote(parsed.path)
            target = path if not target_text else (path.parent / target_text).resolve()
            try:
                target_relative = target.relative_to(root)
            except ValueError:
                target_relative = Path("..") / target.name

            if not target.exists():
                if (relative, target_text) not in KNOWN_MISSING_LINKS:
                    errors.append(
                        f"{relative}:{line_number(text, offset)}: "
                        f"missing local link target {raw_target!r}"
                    )
                continue

            linked[relative].add(target_relative)
            if parsed.fragment and target.suffix.lower() == ".md":
                expected = unquote(parsed.fragment)
                available = anchor_cache.setdefault(target, anchors(target))
                if expected not in available:
                    errors.append(
                        f"{relative}:{line_number(text, offset)}: "
                        f"missing Markdown anchor #{expected} in {target_relative}"
                    )

    return errors, linked


def repository_packages(root: Path) -> dict[str, str]:
    contents: defaultdict[str, list[str]] = defaultdict(list)
    for relative in tracked(root, "*.go"):
        if relative.name.endswith("_test.go"):
            continue
        text = (root / relative).read_text(errors="replace")
        match = PACKAGE_RE.search(text)
        if match:
            contents[match.group(1)].append(text)
    return {name: "\n".join(parts) for name, parts in contents.items()}


def check_symbols(root: Path, markdown: list[Path]) -> list[str]:
    errors = []
    packages = repository_packages(root)
    for relative in markdown:
        text = (root / relative).read_text()
        scan_text = without_fences(text)
        for span in CODE_SPAN_RE.finditer(scan_text):
            for match in QUALIFIED_SYMBOL_RE.finditer(span.group(1)):
                package, symbol = match.groups()
                if package not in packages:
                    continue
                if not re.search(rf"\b{re.escape(symbol)}\b", packages[package]):
                    errors.append(
                        f"{relative}:{line_number(scan_text, span.start())}: "
                        f"missing repository symbol {package}.{symbol}"
                    )
    return errors


def check_docs_index(markdown: list[Path], linked: dict[Path, set[Path]]) -> list[str]:
    index = Path("docs/README.md")
    expected = {
        path for path in markdown if path.parent == Path("docs") and path != index
    }
    missing = sorted(expected - linked[index])
    return [f"{index}: missing index entry for {path}" for path in missing]


def main() -> int:
    try:
        root = repo_root()
        markdown = tracked(root, "*.md")
        link_errors, linked = check_links(root, markdown)
        errors = (
            link_errors
            + check_symbols(root, markdown)
            + check_docs_index(markdown, linked)
        )
    except (OSError, subprocess.CalledProcessError, UnicodeError) as error:
        print(f"documentation check failed: {error}", file=sys.stderr)
        return 1

    if errors:
        print("\n".join(errors), file=sys.stderr)
        print(f"documentation check found {len(errors)} error(s)", file=sys.stderr)
        return 1
    print(f"Checked {len(markdown)} tracked Markdown files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
