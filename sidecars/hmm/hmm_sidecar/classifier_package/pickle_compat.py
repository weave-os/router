"""Load latent models pickled by the training pipeline under its module paths."""

from __future__ import annotations

import pickle
from typing import IO, Any

_TRAINING_MODULES = {
    "ml_dev.agent_flow.llm_classifier.bayes_hmm": "hmm_sidecar.classifier_package.bayes_hmm",
    "ml_dev.agent_flow.llm_classifier.codebook": "hmm_sidecar.classifier_package.codebook",
    "ml_dev.hmm_router.interaction_state": "hmm_sidecar.classifier_package.interaction_state",
    "ml_dev.hmm_router.raw_v5_contract": "hmm_sidecar.classifier_package.contract",
}


# Pickled class names are persisted too; serving renamed these.
_TRAINING_CLASS_NAMES = {
    ("ml_dev.hmm_router.raw_v5_contract", "RawV5HMMTrainingContractVersion"): (
        "HMMTrainingContractVersion"
    ),
}


class _ServingUnpickler(pickle.Unpickler):
    def find_class(self, module: str, name: str) -> Any:
        return super().find_class(
            _TRAINING_MODULES.get(module, module),
            _TRAINING_CLASS_NAMES.get((module, name), name),
        )


def load_latent_model(handle: IO[bytes]) -> Any:
    return _ServingUnpickler(handle).load()  # noqa: S301 - artifact is sha-pinned.
