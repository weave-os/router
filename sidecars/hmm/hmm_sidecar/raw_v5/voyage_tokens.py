"""Pinned local Voyage token counting, matching voyageai's tokenizer pipeline.

Only the public tokenizer is downloaded; input text remains in this process.
The Hugging Face cache supports both ordinary first use and preloaded offline runs.
"""

from __future__ import annotations

from functools import lru_cache
from typing import TYPE_CHECKING


if TYPE_CHECKING:
    from tokenizers import Tokenizer


VOYAGE_TOKENIZER_REPOSITORY = "voyageai/voyage-4-large"
VOYAGE_TOKENIZER_REVISION = "bb931c2635a93efe400c24741363d8ff61d7bb32"
VOYAGE_TOKENIZER_SHA256 = "c0382117ea329cdf097041132f6d735924b697924d6f6fc3945713e96ce87539"
VOYAGE_MAX_INPUT_TOKENS = 32_000
VOYAGE_TRUNCATION_MARKER = "\n[truncated for voyage-4-large input limit]\n"


@lru_cache(maxsize=1)
def _voyage_tokenizer() -> Tokenizer:
    raise NotImplementedError("not on the classifier serving path")


def count_voyage_tokens(text: str) -> int:
    raise NotImplementedError("not on the classifier serving path")


def clip_voyage_text(text: str, max_tokens: int = VOYAGE_MAX_INPUT_TOKENS) -> str:
    raise NotImplementedError("not on the classifier serving path")
