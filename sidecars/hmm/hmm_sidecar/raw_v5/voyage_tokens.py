"""Pinned local Voyage token counting, matching voyageai's tokenizer pipeline.

Only the public tokenizer is downloaded; input text remains in this process.
The Hugging Face cache supports both ordinary first use and preloaded offline runs.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    pass


VOYAGE_TOKENIZER_REPOSITORY = "voyageai/voyage-4-large"
VOYAGE_TOKENIZER_REVISION = "bb931c2635a93efe400c24741363d8ff61d7bb32"
VOYAGE_TOKENIZER_SHA256 = (
    "c0382117ea329cdf097041132f6d735924b697924d6f6fc3945713e96ce87539"
)
VOYAGE_MAX_INPUT_TOKENS = 32_000
