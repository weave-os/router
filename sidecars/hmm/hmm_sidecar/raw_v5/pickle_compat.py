"""Load latent models pickled by the training pipeline under its module paths."""

from __future__ import annotations

import pickle
from typing import IO, Any

_TRAINING_MODULES = {
    "ml_dev.agent_flow.llm_classifier.bayes_hmm": "hmm_sidecar.raw_v5.bayes_hmm",
    "ml_dev.agent_flow.llm_classifier.codebook": "hmm_sidecar.raw_v5.codebook",
    "ml_dev.hmm_router.interaction_state": "hmm_sidecar.raw_v5.interaction_state",
    "ml_dev.hmm_router.raw_v5_contract": "hmm_sidecar.raw_v5.raw_v5_contract",
}


class _ServingUnpickler(pickle.Unpickler):
    def find_class(self, module: str, name: str) -> Any:
        return super().find_class(_TRAINING_MODULES.get(module, module), name)


def load_latent_model(handle: IO[bytes]) -> Any:
    return _ServingUnpickler(handle).load()  # noqa: S301 - artifact is sha-pinned.
