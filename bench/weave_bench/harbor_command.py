"""``harbor run`` argv for one (benchmark, arm) job.

One Harbor job per arm; arms differ only in where Codex's API traffic goes
(``ArmSpec``) and, for router arms, the leading ``/beta`` turn. Everything
else — dataset, task list, attempts, concurrency, Codex version, effort — is
identical across arms so a paired comparison is valid.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

from weave_bench.arms import ArmSpec, Upstream
from weave_bench.benchmarks import ATLAS_JUDGE_BASE_URL, ATLAS_JUDGE_MODEL, ATLAS_QA_SUBDIR, PINS, Benchmark
from weave_bench.codex_config import codex_config_for_arm
from weave_bench.config import BenchConfig

BETA_CODEX_AGENT = "weave_bench.agent.beta_codex:BetaCodex"
# Harbor's Codex agent always authenticates via OPENAI_API_KEY; the launcher
# exports the arm's key under this coordinator-side name and Harbor templates it.
AGENT_API_KEY_ENV = "WEAVE_BENCH_AGENT_API_KEY"
# Atlas ``[verifier.env]`` defaults to ``${OPENAI_API_KEY}``/``${OPENAI_API_BASE}``
# for the judge, which would collide with the agent's key; these overrides
# point the judge at its own variable. Harbor expands ``${VAR}`` from the
# coordinator environment at trial time, so no secret lands in argv/config.
JUDGE_API_KEY_ENV = "WEAVE_BENCH_JUDGE_API_KEY"
# Codex's own base-URL overrides must not leak into the sandbox: every
# non-direct arm sets its endpoint via ``config.toml``, direct arms use OpenAI's default.
INHERITED_ENV_TO_CLEAR = ("OPENAI_BASE_URL", "OPENAI_API_BASE")


def job_name(run_id: str, arm: ArmSpec) -> str:
    return f"{run_id}--{arm.name}"


@dataclass(frozen=True)
class HarborJob:
    benchmark: Benchmark
    arm: ArmSpec
    run_id: str
    argv: tuple[str, ...]
    # Coordinator-side variables Harbor templates into ``--agent-env`` /
    # ``--verifier-env``; values are looked up at launch, never stored.
    env_from: dict[str, str]
    codex_config_path: Path | None
    codex_config: str | None


def arm_api_key_env(arm: ArmSpec, config: BenchConfig) -> str:
    if arm.via_router:
        return config.router.api_key_env
    if arm.upstream is Upstream.OPENROUTER:
        return config.providers.openrouter_api_key_env
    return config.providers.openai_api_key_env


def agent_host(arm: ArmSpec, config: BenchConfig) -> str | None:
    """Endpoint host merged into the sandbox egress allowlist for non-direct arms."""
    if arm.upstream is Upstream.OPENROUTER:
        return urlsplit(config.openrouter_tap.public_url).hostname
    if arm.upstream is Upstream.DIRECT:
        return None
    return urlsplit(config.router.base_url).hostname


def build_job(
    benchmark: Benchmark,
    arm: ArmSpec,
    *,
    run_id: str,
    config: BenchConfig,
    task_names: list[str],
    n_attempts: int,
    n_concurrent: int,
    codex_version: str | None = None,
) -> HarborJob:
    pins = PINS[benchmark]
    jobs_dir = Path(config.harbor.jobs_dir)
    name = job_name(run_id, arm)
    argv: list[str] = ["harbor", "run"]
    if pins.harbor_dataset is None:
        argv += ["--path", str(Path(config.harbor.atlas_checkout_dir) / ATLAS_QA_SUBDIR)]
    else:
        argv += ["--dataset", pins.harbor_dataset]
    argv += [
        "--env",
        config.harbor.environment.value,
        "--agent",
        BETA_CODEX_AGENT,
        "--model",
        arm.harbor_model.value,
        "--n-attempts",
        str(n_attempts),
        "--n-concurrent",
        str(n_concurrent),
        "--jobs-dir",
        str(jobs_dir),
        "--job-name",
        name,
        "--agent-kwarg",
        f"version={codex_version or pins.codex_version}",
        "--agent-kwarg",
        f"reasoning_effort={arm.reasoning_effort.value}",
        "--agent-env",
        f"OPENAI_API_KEY=${{{AGENT_API_KEY_ENV}}}",
    ]
    env_from = {AGENT_API_KEY_ENV: arm_api_key_env(arm, config)}
    codex_config = codex_config_for_arm(
        arm,
        run_id=run_id,
        router_base_url=config.router.base_url,
        tap_public_url=config.openrouter_tap.public_url,
    )
    codex_config_path = None
    if codex_config is not None:
        codex_config_path = jobs_dir / f"{name}.codex-config.toml"
        argv += ["--agent-kwarg", f"config={codex_config_path}"]
    if arm.beta_preflight:
        argv += ["--agent-kwarg", "beta_toggle=true"]
    host = agent_host(arm, config)
    if host is not None:
        argv += ["--allow-agent-host", host]
    if benchmark is Benchmark.ATLAS_QNA:
        argv += [
            "--verifier-env",
            f"EVAL_API_KEY=${{{JUDGE_API_KEY_ENV}}}",
            "--verifier-env",
            f"EVAL_BASE_URL={ATLAS_JUDGE_BASE_URL}",
            "--verifier-env",
            f"EVAL_MODEL={ATLAS_JUDGE_MODEL}",
        ]
        env_from[JUDGE_API_KEY_ENV] = config.providers.judge_api_key_env
    argv += ["--yes", "--quiet"]
    for task_name in task_names:
        argv += ["--include-task-name", task_name]
    return HarborJob(benchmark, arm, run_id, tuple(argv), env_from, codex_config_path, codex_config)
