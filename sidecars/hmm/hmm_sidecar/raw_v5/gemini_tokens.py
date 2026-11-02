"""Local Gemini Embedding 2 input sizing without a token-count API request.

Budget for one token per UTF-8 byte, leaving 192 tokens below the provider
limit for framing. The cap covers the complete serialized document, including
JSON escaping and truncation markers; it does not assume an average token size.
"""

from __future__ import annotations

from typing import Final

GEMINI_EMBEDDING_MAX_INPUT_TOKENS: Final = 8_100
GEMINI_EMBEDDING_PROVIDER_MAX_INPUT_TOKENS: Final = 8_192
GEMINI_EMBEDDING_MAX_INPUT_BYTES: Final = 8_000


def gemini_input_fits_byte_budget(text: str) -> bool:
    return len(text.encode("utf-8")) <= GEMINI_EMBEDDING_MAX_INPUT_BYTES
