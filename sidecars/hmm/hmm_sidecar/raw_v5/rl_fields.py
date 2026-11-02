"""Shared RL action-space schema fields."""

from __future__ import annotations

from typing import Self

from pydantic import Field, model_validator

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

    @model_validator(mode="after")
    def check_action_space_contract(self) -> Self:
        if (
            self.action_space_size is not None
            and self.output_action_count is not None
            and self.action_space_size != self.output_action_count
        ):
            raise ValueError("rl action_space_size must equal output_action_count")
        if self.action_space_models:
            model_count = len(self.action_space_models)
            if (
                self.action_space_size is not None
                and self.action_space_size != model_count
            ):
                raise ValueError(
                    "rl action_space_size must match action_space_models length"
                )
            if (
                self.output_action_count is not None
                and self.output_action_count != model_count
            ):
                raise ValueError(
                    "rl output_action_count must match action_space_models length"
                )
        if (
            self.observed_action_count is not None
            and self.action_space_size is not None
            and self.observed_action_count > self.action_space_size
        ):
            raise ValueError("rl observed_action_count cannot exceed action_space_size")
        if self.zero_support_action_count is not None:
            if len(self.zero_support_actions) != self.zero_support_action_count:
                raise ValueError(
                    "rl zero_support_action_count must match "
                    "zero_support_actions length"
                )
        return self
