#!/usr/bin/env python3
"""Regression tests for the repository documentation checker."""

from __future__ import annotations

import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path

from scripts import check_docs


class CheckDocsTests(unittest.TestCase):
    def test_tracked_scan_skips_deleted_sources_without_hiding_broken_links(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "current.go").write_text("package current\n")
            with patch("subprocess.check_output", return_value=b"removed.go\0current.go\0"):
                self.assertEqual(check_docs.tracked(root, "*.go"), [Path("current.go")])
            (root / "doc.md").write_text("[removed](removed.go)\n")
            errors, _ = check_docs.check_links(root, [Path("doc.md")])
            self.assertEqual(errors, ["doc.md:1: missing local link target 'removed.go'"])

    def test_link_diagnostic_uses_original_line_after_fenced_block(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "doc.md").write_text(
                "before\n"
                "```\n"
                "[ignored](missing-in-fence.md)\n"
                "```\n"
                "after\n"
                "[broken](missing.md)\n"
            )

            errors, _ = check_docs.check_links(root, [Path("doc.md")])

        self.assertEqual(
            errors,
            ["doc.md:6: missing local link target 'missing.md'"],
        )


if __name__ == "__main__":
    unittest.main()
