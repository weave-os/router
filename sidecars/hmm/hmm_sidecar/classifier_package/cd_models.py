"""Domain models for conversation dataset extraction."""

from __future__ import annotations

from typing import Literal, TypeVar

from pydantic import BaseModel

ConversationOutcome = Literal["completed", "abandoned", "blocked", "ongoing"]
GoalCategory = Literal[
    "fix_pr_review_comments",
    "bug_fix",
    "create_pr",
    "feature_implementation",
    "debugging_investigation",
    "refactor",
    "docs",
    "other",
]
FrictionCategory = Literal[
    "wrong_approach",
    "buggy_code",
    "misunderstood_request",
    "excessive_changes",
    "api_error",
    "user_rejected_action",
    "none",
]


class Classification(BaseModel, frozen=True):
    """Conversation-level classification labels."""

    conversation_outcome: ConversationOutcome | None
    goal_category: GoalCategory | None
    friction_category: FrictionCategory | None


T = TypeVar("T")
