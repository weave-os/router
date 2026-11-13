"""Harbor Codex agent with an optional leading ``/beta`` turn.

Imported by the Harbor coordinator via ``--agent weave_bench.agent.beta_codex:BetaCodex``.
Everything but the extra turn — Codex version, model, flags, config, transcript
capture — is Harbor's stock Codex agent, so the control arms run the same class
with ``beta_toggle=false`` and the only difference between arms is one router
command.
"""

from __future__ import annotations

from enum import StrEnum
from typing import Any

from harbor.agents.installed.codex import Codex
from harbor.environments.base import BaseEnvironment, ExecResult
from harbor.models.trial.paths import EnvironmentPaths

from weave_bench.agent.beta_turn import BETA_TURN_OUTPUT_FILENAME, is_fresh_exec, with_beta_turn


class BetaToggle(StrEnum):
    """The ``beta_toggle`` agent kwarg as Harbor hands it over from ``--agent-kwarg``."""

    TRUE = "true"
    FALSE = "false"


class BetaCodex(Codex):
    def __init__(self, *args: Any, beta_toggle: BetaToggle = BetaToggle.FALSE, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self._beta_toggle = BetaToggle(str(beta_toggle).lower()) is BetaToggle.TRUE

    async def exec_as_agent(
        self,
        environment: BaseEnvironment,
        command: str,
        env: dict[str, str] | None = None,
        cwd: str | None = None,
        timeout_sec: int | None = None,
    ) -> ExecResult:
        if self._beta_toggle and is_fresh_exec(command):
            command = with_beta_turn(command, (EnvironmentPaths.agent_dir / BETA_TURN_OUTPUT_FILENAME).as_posix())
        return await super().exec_as_agent(environment, command, env=env, cwd=cwd, timeout_sec=timeout_sec)
