"""Domain models for conversation dataset extraction."""

from __future__ import annotations

from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Generic, Literal, TypeVar

from pydantic import BaseModel

Role = Literal["user", "agent"]
ConversationOutcome = Literal[
    "completed", "abandoned", "blocked", "ongoing"
]
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
MessageType = Literal["response", "thought"]

DATETIME_MIN_UTC = datetime.min.replace(tzinfo=UTC)


class PromptInitiator(StrEnum):
    """Reported actor for a captured prompt."""

    HUMAN = "human"
    AUTOMATION = "automation"


class PromptOrigin(StrEnum):
    """Submission provenance reported by the source client."""

    UNKNOWN = "unknown"
    USER_SUBMISSION = "user_submission"
    AUTOMATION = "automation"


class PromptProvenance(BaseModel, frozen=True):
    """Captured signals; missing historical evidence is not a human assertion."""

    prompt_initiator: PromptInitiator | None = None
    prompt_origin: PromptOrigin | None = None
    is_system_message: bool | None = None
    redacted_at: datetime | None = None
    subagent_id: str | None = None
    parent_session_id: str | None = None

    def provenance_fields(self) -> dict[str, str | bool]:
        raise NotImplementedError("not on the classifier serving path")


class PromptRow(PromptProvenance):
    """A single user prompt row from the database."""

    external_account_id: str
    conversation_id: str
    generation_id: str | None
    session_id: str
    timestamp: datetime
    prompt_content: str
    prompt_length: int | None
    git_branch: str | None
    git_repo_remote: str | None
    model: str | None = None
    source_context: str | None = None


class AgentMessageRow(BaseModel, frozen=True):
    """A single agent message row from the database."""

    cursor_plugin_account_id: str
    conversation_id: str
    generation_id: str | None
    timestamp: datetime
    message_type: MessageType
    content: str | None
    content_length: int | None
    duration_ms: int | None
    model: str | None


class ToolCallRow(BaseModel, frozen=True):
    """A single tool call row from the database."""

    cursor_plugin_account_id: str
    conversation_id: str
    generation_id: str | None
    timestamp: datetime
    tool_name: str
    tool_type: str
    tool_input: str | None
    success: bool | None
    duration_ms: int | None
    git_branch: str | None
    git_repo_remote: str | None
    model: str | None = None


class ToolCallEntry(BaseModel, frozen=True):
    """Simplified tool call for trace assembly."""

    tool_name: str
    tool_type: str
    tool_input: str | None = None
    success: bool | None
    duration_ms: int | None
    model: str | None = None


class AgentMessageEntry(BaseModel, frozen=True):
    """Simplified agent message for trace assembly."""

    message_type: MessageType
    content: str
    timestamp: datetime
    model: str | None = None
    duration_ms: int | None = None


class UserMessage(PromptProvenance):
    """A user message within a conversation trace."""

    turn_index: int
    role: Literal["user"] = "user"
    content: str
    timestamp: datetime
    model: str | None = None


class AgentTurn(BaseModel, frozen=True):
    """An agent turn containing messages and tool calls."""

    turn_index: int
    role: Literal["agent"] = "agent"
    messages: tuple[AgentMessageEntry, ...]
    tool_calls: tuple[ToolCallEntry, ...] = ()

    @property
    def response_content(self) -> str:
        raise NotImplementedError("not on the classifier serving path")


TraceEntry = UserMessage | AgentTurn


class ConversationTrace(BaseModel, frozen=True):
    """Ordered sequence of user messages and agent turns."""

    entries: tuple[TraceEntry, ...]

    @property
    def turn_count(self) -> int:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def user_messages(self) -> tuple[UserMessage, ...]:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def agent_turns(self) -> tuple[AgentTurn, ...]:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def started_at(self) -> datetime:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def ended_at(self) -> datetime:
        raise NotImplementedError("not on the classifier serving path")


class RepoInstallation(BaseModel, frozen=True):
    """A GitHub repository installation record."""

    organization_id: str
    base_url: str
    repository_id: str
    owner_name: str
    repo_name: str


class PullRequestRecord(BaseModel, frozen=True):
    """A merged pull request record."""

    organization_id: str
    base_url: str
    repository_id: str
    pr_id: int
    title: str
    url: str
    head_branch: str
    merged_at: datetime


class EstimateValues(BaseModel, frozen=True):
    """PR output estimate values from v1, v2, and manual override."""

    v1: float | None
    v2: float | None
    override: float | None


class MatchedPR(BaseModel, frozen=True):
    """A pull request matched to a conversation with estimates."""

    pr_id: int
    pr_title: str
    pr_url: str
    head_branch: str
    merged_at: datetime
    base_url: str
    repository_id: str
    output_score: float | None
    original_estimate: float | None
    overridden_estimate: float | None
    pr_additions: int | None
    pr_deletions: int | None


class Classification(BaseModel, frozen=True):
    """Conversation-level classification labels."""

    conversation_outcome: ConversationOutcome | None
    goal_category: GoalCategory | None
    friction_category: FrictionCategory | None


class ConversationDatasetRow(BaseModel, frozen=True):
    """A complete dataset row linking conversation to PR."""

    source: str = "cursor"
    organization_id: str
    cursor_plugin_account_id: str
    session_id: str
    conversation_id: str
    model: str | None
    user_email: str | None
    pr_match_status: str = "matched"
    git_branch: str | None = None
    git_repo_remote: str | None = None
    source_context: str | None = None
    router_calls: tuple[dict[str, Any], ...] = ()
    trace: ConversationTrace
    pr: MatchedPR | None
    classification: Classification


class OrgDiagnostics(BaseModel, frozen=True):
    """Extraction diagnostics for a single organization."""

    source: str = "cursor"
    organization_id: str
    conversations_found: int
    conversations_with_pr: int
    skipped_no_branch: int
    skipped_no_repo: int
    skipped_no_pr: int


T = TypeVar("T")


class Success(BaseModel, Generic[T], frozen=True):
    """Successful result wrapper."""

    value: T


class Failure(BaseModel, frozen=True):
    """Failed result wrapper with error details."""

    error: str
    context: str = ""
    turn_index: int | None = None


Result = Success[T] | Failure
