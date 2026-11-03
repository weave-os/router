"""Frozen raw-V5 serving codecs from 31602cb06bcb7e312680be4863f670b6b90dde82.

Only known historical encoder identities select these builders. New training uses
the current token-budgeted builders; accepted old packages retain their original
8,000-byte Gemini and 20,000-character Voyage projections.
"""

from __future__ import annotations

RAW_V5_GEMINI_EMBEDDING_2_MAX_SERIALIZED_BYTES = 8_000
