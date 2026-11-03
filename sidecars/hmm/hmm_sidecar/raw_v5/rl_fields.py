"""Shared RL action-space schema fields."""

from __future__ import annotations


from pydantic import Field

from hmm_sidecar.raw_v5.weave_data import WeaveFeatureContract
from hmm_sidecar.raw_v5.schema_base import FrozenClosedModel, NonEmptyString


class RLActionSpaceContract(FrozenClosedModel):
    """Common action-space contract shared by RL runs and RL model artifacts."""

    behavior_policy_ids: tuple[NonEmptyString, ...] = Field(min_length=1)
    target_policy_id: NonEmptyString | None
    reward_version: NonEmptyString
    prompt_format_version: NonEmptyString
    model_roster_version: NonEmptyString
    cost_table_version: NonEmptyString
    feature_contract: WeaveFeatureContract | None = None
    action_space_id: NonEmptyString | None = None
    action_space_models: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    action_space_size: int | None = Field(default=None, ge=1)
    output_action_count: int | None = Field(default=None, ge=1)
    observed_action_count: int | None = Field(default=None, ge=0)
    zero_support_action_count: int | None = Field(default=None, ge=0)
    zero_support_actions: tuple[NonEmptyString, ...] = Field(default_factory=tuple)
    requires_retrain_on_action_space_mismatch: bool = True
