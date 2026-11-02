"""Shared feature-arm names for training and lightweight CLI reports.

Also the dependency-free HMM-chain and element-lookback layout vocabulary the
classifier contracts validate, so serving and the Qwen Modal runtime can read a
lookback layout without importing the interaction-state math (sklearn).
"""

from collections.abc import Mapping
from dataclasses import dataclass
from enum import StrEnum
from typing import Any


class HMMChain(StrEnum):
    ELEMENT = "element"
    INTERACTION_STATE = "interaction_state"


class ElementLookbackSource(StrEnum):
    """Which vectors fill an interaction-state classifier's element_lookback slots.

    Training writes only stored per-message vectors: XGBoost never receives
    PCA-reduced inputs. PCA_PROJECTION exists so the one artifact built before
    that rule (its lookback block carries no source) can still be served.
    """

    STORED_VECTORS = "stored_vectors"
    PCA_PROJECTION = "pca_projection"


HMM_CHAIN_MODEL_INFO_KEY = "hmm_chain"
ELEMENT_LOOKBACK_BLOCK_NAME = "element_lookback"


def hmm_chain_of(model_info: dict[str, Any] | None) -> HMMChain:
    """Bundles written before this chain existed carry no key and are element chains."""
    if not isinstance(model_info, dict):
        return HMMChain.ELEMENT
    return HMMChain(model_info.get(HMM_CHAIN_MODEL_INFO_KEY, HMMChain.ELEMENT))


def element_lookback_layout_block(
    *,
    lookback_rounds: int,
    user_dim: int,
    agent_dim: int,
) -> dict[str, Any]:
    names = [
        name
        for slot in range(1, lookback_rounds)
        for name in (
            *(f"lookback_{slot}_user_{index}" for index in range(user_dim)),
            *(f"lookback_{slot}_agent_{index}" for index in range(agent_dim)),
            f"lookback_{slot}_present",
        )
    ]
    return {"block": ELEMENT_LOOKBACK_BLOCK_NAME, "dim": len(names), "names": names}


def stored_vector_lookback_layout_block(
    *, lookback_rounds: int, embedding_dim: int
) -> dict[str, Any]:
    raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class ElementLookback:
    """A classifier's element_lookback block resolved to the vectors its slots hold.

    ``rounds`` counts the current round, so the block holds ``rounds - 1`` slots.
    """

    rounds: int
    source: ElementLookbackSource
    user_dim: int
    agent_dim: int

    def layout_block(self) -> dict[str, Any]:
        if self.source is ElementLookbackSource.STORED_VECTORS:
            return stored_vector_lookback_layout_block(
                lookback_rounds=self.rounds, embedding_dim=self.user_dim
            )
        return element_lookback_layout_block(
            lookback_rounds=self.rounds,
            user_dim=self.user_dim,
            agent_dim=self.agent_dim,
        )


def resolve_element_lookback(block: object, *, embedding_dim: int) -> ElementLookback:
    """Resolve a declared lookback block, failing closed unless it is exactly one known layout.

    A block naming its source must be the stored-vector layout. A block without
    one predates the field and reads as the legacy per-role PCA layout at the
    per-slot widths its names declare; callers holding the HMM bundle must
    confirm those widths are its projector's.
    """
    # A non-mapping block resolves as empty, which the final layout check rejects.
    fields: Mapping[str, Any] = block if isinstance(block, Mapping) else {}
    declared_names = fields.get("names")
    names = declared_names if isinstance(declared_names, list) else []
    rounds = sum(1 for name in names if str(name).endswith("_present")) + 1
    declared_source = fields.get("source")
    if declared_source is None:
        lookback = ElementLookback(
            rounds=rounds,
            source=ElementLookbackSource.PCA_PROJECTION,
            user_dim=sum(
                1 for name in names if str(name).startswith("lookback_1_user_")
            ),
            agent_dim=sum(
                1 for name in names if str(name).startswith("lookback_1_agent_")
            ),
        )
    elif declared_source == ElementLookbackSource.STORED_VECTORS:
        lookback = ElementLookback(
            rounds=rounds,
            source=ElementLookbackSource.STORED_VECTORS,
            user_dim=embedding_dim,
            agent_dim=embedding_dim,
        )
    else:
        raise ValueError(
            f"element_lookback source {declared_source!r} is not a layout training writes"
        )
    if rounds < 2 or dict(fields) != lookback.layout_block():
        raise ValueError(
            "element_lookback block is neither the stored-vector layout nor the legacy per-role PCA layout"
        )
    return lookback
