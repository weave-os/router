"""Pinned local Voyage token counting, matching voyageai's tokenizer pipeline.

Only the public tokenizer is downloaded; input text remains in this process.
The Hugging Face cache supports both ordinary first use and preloaded offline runs.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    pass
