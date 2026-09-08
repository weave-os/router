"""``BetaCodex`` plus a working-tree patch export, for SWE-Bench Pro.

Harbor's in-sandbox verifier is only a cross-check; the publishable verdict is
Scale's unmodified ``swe_bench_pro_eval.py`` run over the agent's patch. Codex
leaves its edits in ``/app`` and Harbor copies nothing out, so after the task
turn this stages everything and writes ``git diff --cached`` into the agent log
dir (mirrored to the trial's ``agent/`` folder), then unstages so the verifier
sees the tree exactly as Codex left it.
"""

from __future__ import annotations

from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.trial.paths import EnvironmentPaths

from weave_bench.agent.beta_codex import BetaCodex

REPO_DIR = "/app"
MODEL_PATCH_FILENAME = "model.patch"
MODEL_PATCH_PATH = (EnvironmentPaths.agent_dir / MODEL_PATCH_FILENAME).as_posix()
# ``--binary`` keeps the patch ``git apply``-able when Codex touched fixtures.
PATCH_EXPORT_COMMAND = (
    f"cd {REPO_DIR} && git add -A && git diff --cached --binary HEAD > {MODEL_PATCH_PATH}; git reset -q"
)


class ProCodex(BetaCodex):
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        try:
            await super().run(instruction, environment, context)
        finally:
            await self.exec_as_agent(environment, command=PATCH_EXPORT_COMMAND)
