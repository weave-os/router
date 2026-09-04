#!/usr/bin/env python3
"""Regression tests for the repository documentation checker."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from scripts import check_docs


class CheckDocsTests(unittest.TestCase):
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
