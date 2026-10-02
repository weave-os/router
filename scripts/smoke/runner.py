#!/usr/bin/env python3
"""Own exactly one disposable Compose project; never adopt a developer stack."""

from __future__ import annotations

from enum import StrEnum
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from typing import Literal
from uuid import uuid4

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "scripts"))
from agent_checks import (  # noqa: E402 - standalone script needs scripts/ on sys.path
    PROXY_MODE_ENV,
    ProxyMode,
    SuiteID,
    run_command,
    validation_environment,
)

OWNER_LABEL = "ai.weave.smoke-owner"
PLACEHOLDER_KEY = "smoke-fixture-key-unused-outside-replay"
COMMAND_TIMEOUT_SECONDS = 120
BUILD_TIMEOUT_SECONDS = 1200
BOOT_TIMEOUT_SECONDS = 180
TEST_TIMEOUT_SECONDS = 600
HEALTH_TIMEOUT_SECONDS = 180
HEALTH_POLL_SECONDS = 2
ROUTER_PORT = 8080
MINIMUM_COMPOSE_VERSION = (2, 24, 4)
MODEL_OVERRIDE_ENVIRONMENTS = ("SMOKE_PIN_MODEL", "SMOKE_OPENAI_PIN_MODEL")
CLIENT_ENVIRONMENTS = (
    "SMOKE_ROUTER_KEY",
    "SMOKE_BASE_URL",
    "SMOKE_OPENAI_ENABLED",
    *MODEL_OVERRIDE_ENVIRONMENTS,
)
SMOKE_DEPLOYMENT_MODE: Literal["selfhosted"] = "selfhosted"
SMOKE_ROUTER_STRATEGY: Literal["cluster"] = "cluster"


class DockerResourceKind(StrEnum):
    CONTAINER = "container"
    VOLUME = "volume"
    NETWORK = "network"


class ComposeService(StrEnum):
    POSTGRES = "postgres"
    PUBSUB = "pubsub-emulator"
    MIGRATE = "migrate"
    SERVER = "server"
    SEED = "seed"
    MITMPROXY = "mitmproxy"
    INGRESS = "smoke-ingress"


BUILT_SERVICES = (ComposeService.SERVER, ComposeService.SEED, ComposeService.MITMPROXY)


class SmokePhase(StrEnum):
    BUILD = "Build"
    BOOT = "Boot + health"
    SEED = "Seed"
    ASSERTIONS = "Assertions"


class ProbePath(StrEnum):
    STARTUP = "/startupz"
    LIVENESS = "/health"
    CAPACITY = "/capacityz"


def log(message: str) -> None:
    print(f"[smoke] {message}", flush=True)


class SmokeRun:
    def __init__(self) -> None:
        self.mode = ProxyMode(os.environ.get(PROXY_MODE_ENV, ProxyMode.REPLAY.value))
        if os.environ.get("SMOKE_BASE_URL"):
            raise ValueError(
                "SMOKE_BASE_URL is test-client-only; the runner always allocates its own localhost port"
            )
        if self.mode != ProxyMode.REPLAY and not os.environ.get("ANTHROPIC_API_KEY"):
            raise ValueError("recording needs an explicitly exported ANTHROPIC_API_KEY")
        self.keep = os.environ.get("SMOKE_KEEP_STACK") == "1"
        self.prebuilt = os.environ.get("SMOKE_PREBUILT") == "1"
        self.owner = uuid4().hex
        self.project = f"router-smoke-{self.owner}"
        self.directory: Path | None = None
        self.compose: list[str] = []
        self.started = False
        self.phase_seconds: dict[SmokePhase, str] = {}
        if os.environ.get("SMOKE_BUILD_SECONDS"):
            self.phase_seconds[SmokePhase.BUILD] = os.environ["SMOKE_BUILD_SECONDS"]
        # Direct `make smoke` has the same authority boundary as agent checks.
        self.environment = {
            key: value
            for key, value in validation_environment(SuiteID.SMOKE).items()
            if not key.startswith("COMPOSE_")
        }
        self.environment[PROXY_MODE_ENV] = self.mode.value
        self.environment["COMPOSE_DISABLE_ENV_FILE"] = "1"
        self.record_credentials: dict[str, str] = {}
        if self.mode != ProxyMode.REPLAY:
            self.record_credentials["SMOKE_RECORD_ANTHROPIC_KEY"] = os.environ[
                "ANTHROPIC_API_KEY"
            ]
            self.record_credentials["SMOKE_RECORD_OPENAI_KEY"] = os.environ.get(
                "OPENAI_API_KEY", ""
            )

    def command(
        self,
        *arguments: str,
        capture: bool = False,
        check: bool = True,
        environment: dict[str, str] | None = None,
        timeout: float | None = None,
    ) -> subprocess.CompletedProcess:
        command_environment = dict(
            environment if environment is not None else self.environment
        )
        if arguments[:2] == ("docker", "compose"):
            if any(
                action in arguments
                for action in ("config", "up", "run", "down", "port", "logs")
            ):
                command_environment.update(self.record_credentials)
            if "build" in arguments and os.environ.get("SMOKE_CI_CACHE") == "1":
                # This credential authorizes the CI cache, never provider calls.
                if "ACTIONS_RUNTIME_TOKEN" in os.environ:
                    command_environment["ACTIONS_RUNTIME_TOKEN"] = os.environ[
                        "ACTIONS_RUNTIME_TOKEN"
                    ]
        completed = run_command(
            arguments,
            cwd=REPO_ROOT,
            env=command_environment,
            text=True,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
            timeout=timeout if timeout is not None else COMMAND_TIMEOUT_SECONDS,
        )
        if check:
            completed.check_returncode()
        return completed

    def resource_ids(self, kind: DockerResourceKind) -> list[str]:
        flags = ["--all"] if kind == DockerResourceKind.CONTAINER else []
        return self.command(
            "docker",
            kind,
            "ls",
            *flags,
            "--quiet",
            "--filter",
            f"label=com.docker.compose.project={self.project}",
            capture=True,
        ).stdout.split()

    def require_owned_resources(self, *, empty: bool = False) -> None:
        for kind in DockerResourceKind:
            identifiers = self.resource_ids(kind)
            if not identifiers:
                continue
            if empty:
                raise RuntimeError(
                    f"refusing to reuse an existing Compose project: {self.project}"
                )
            labels = (
                ".Config.Labels" if kind == DockerResourceKind.CONTAINER else ".Labels"
            )
            owners = self.command(
                "docker",
                kind,
                "inspect",
                "--format",
                "{{ index " + labels + ' "' + OWNER_LABEL + '" }}',
                *identifiers,
                capture=True,
            ).stdout.splitlines()
            if len(owners) != len(identifiers) or any(
                owner != self.owner for owner in owners
            ):
                raise RuntimeError(
                    f"refusing cleanup: unowned {kind} in {self.project}"
                )

    def prepare(self) -> None:
        version = self.command(
            "docker", "compose", "version", "--short", capture=True
        ).stdout.strip()
        parsed = re.match(r"v?(\d+)\.(\d+)\.(\d+)", version)
        if not parsed or tuple(map(int, parsed.groups())) < MINIMUM_COMPOSE_VERSION:
            raise RuntimeError(
                f"smoke isolation requires Docker Compose >= {'.'.join(map(str, MINIMUM_COMPOSE_VERSION))} (!override support)"
            )
        self.require_owned_resources(empty=True)
        self.directory = Path(tempfile.mkdtemp(prefix="router-smoke-"))
        override = self.directory / "isolation.yml"
        override.write_text(self.isolation_config())
        override.chmod(0o600)
        self.compose = [
            "docker",
            "compose",
            "--project-name",
            self.project,
            "--project-directory",
            str(REPO_ROOT),
            "--env-file",
            os.devnull,
            "-f",
            str(REPO_ROOT / "docker-compose.yml"),
            "-f",
            str(REPO_ROOT / "smoke/mitmproxy/docker-compose.yml"),
            "-f",
            str(override),
        ]
        if os.environ.get("SMOKE_CI_CACHE") == "1":
            self.compose += [
                "-f",
                str(REPO_ROOT / "smoke/mitmproxy/docker-compose.ci-cache.yml"),
            ]
        self.command(*self.compose, "config", "--quiet")
        log(f"isolated project {self.project}; state {self.directory}")

    def isolation_config(self) -> str:
        replay = self.mode == ProxyMode.REPLAY
        server_environment = {
            "DATABASE_URL": "postgres://router:router@postgres:5432/router?sslmode=disable",
            "PORT": str(ROUTER_PORT),
            "ROUTER_DEPLOYMENT_MODE": SMOKE_DEPLOYMENT_MODE,
            "ROUTER_DEFAULT_STRATEGY": SMOKE_ROUTER_STRATEGY,
            "ROUTER_ONNX_ASSETS_DIR": "/opt/router/assets",
            "ROUTER_ONNX_LIBRARY_DIR": "/usr/lib",
            "PUBSUB_EMULATOR_HOST": "pubsub-emulator:8085",
            "PUBSUB_PROJECT_ID": "router-local",
            "PUBSUB_TOPIC_ROUTER_INVALIDATION": "router-installation-invalidate",
            "PUBSUB_SUBSCRIPTION_ROUTER_INVALIDATION": "router-installation-invalidate",
            "HTTPS_PROXY": "http://mitmproxy:8888",
            "HTTP_PROXY": "http://mitmproxy:8888",
            "NO_PROXY": "pubsub-emulator,postgres,localhost,127.0.0.1",
            "SSL_CERT_DIR": "/certs",
            "ANTHROPIC_API_KEY": (
                PLACEHOLDER_KEY if replay else "${SMOKE_RECORD_ANTHROPIC_KEY:-}"
            ),
            "OPENAI_API_KEY": (
                PLACEHOLDER_KEY if replay else "${SMOKE_RECORD_OPENAI_KEY:-}"
            ),
        }
        lines = ["services:"]
        for service in ComposeService:
            lines += [
                f"  {service}:",
                f"    labels: {json.dumps({OWNER_LABEL: self.owner})}",
            ]
            if service in (
                ComposeService.POSTGRES,
                ComposeService.PUBSUB,
                ComposeService.SERVER,
            ):
                lines.append("    ports: !reset []")
            if service in BUILT_SERVICES:
                image_name = (
                    f"router-{service}"
                    if self.prebuilt
                    else f"{self.project}-{service}"
                )
                lines.append(f"    image: {image_name}")
                lines += [
                    "    build:",
                    f"      labels: {json.dumps({OWNER_LABEL: self.owner})}",
                ]
            if service == ComposeService.INGRESS:
                # Internal Docker networks do not publish host ports. This
                # relay has no proxy protocol or arbitrary upstream selection.
                lines += [
                    "    image: postgres:15-alpine",
                    f"    entrypoint: {json.dumps(['/bin/busybox', 'nc', '-lk', '-p', str(ROUTER_PORT), '-e', '/bin/busybox', 'nc', ComposeService.SERVER, str(ROUTER_PORT)])}",
                    "    networks: [default, ingress]",
                    f'    ports: !override ["127.0.0.1::{ROUTER_PORT}"]',
                    "    read_only: true",
                    "    cap_drop: [ALL]",
                    "    security_opt: [no-new-privileges:true]",
                ]
            if service == ComposeService.SERVER:
                lines += [
                    "    env_file: !reset []",
                    f"    environment: !override {json.dumps(server_environment)}",
                ]
            if service == ComposeService.MITMPROXY:
                cassette_mode: Literal["ro", "rw"] = "ro" if replay else "rw"
                lines += [
                    "    volumes: !override",
                    "      - mitm_certs:/certs",
                    f"      - ./smoke/mitmproxy/cassettes:/cassettes:{cassette_mode}",
                    "    environment:",
                    f"      {PROXY_MODE_ENV}: {self.mode.value}",
                ]
        lines += [
            "  hmm-sidecar:",
            "    env_file: !reset []",
            "volumes:",
            "  router_postgres_data:",
            f"    labels: {json.dumps({OWNER_LABEL: self.owner})}",
            "  mitm_certs:",
            f"    labels: {json.dumps({OWNER_LABEL: self.owner})}",
            "networks:",
            "  default:",
            f"    internal: {str(replay).lower()}",
            f"    labels: {json.dumps({OWNER_LABEL: self.owner})}",
            "  ingress:",
            f"    labels: {json.dumps({OWNER_LABEL: self.owner})}",
        ]
        return "\n".join(lines) + "\n"

    def run(self) -> None:
        self.prepare()
        if not self.prebuilt:
            started = time.monotonic()
            self.command(
                *self.compose,
                "build",
                *BUILT_SERVICES,
                timeout=BUILD_TIMEOUT_SECONDS,
            )
            self.phase_seconds[SmokePhase.BUILD] = f"{time.monotonic() - started:.1f}"
        started = time.monotonic()
        self.started = True  # An interrupted/failed up can still leave owned resources.
        self.command(
            *self.compose,
            "up",
            "-d",
            ComposeService.SERVER,
            ComposeService.MITMPROXY,
            ComposeService.INGRESS,
            timeout=BOOT_TIMEOUT_SECONDS,
        )
        binding = self.command(
            *self.compose,
            "port",
            ComposeService.INGRESS,
            str(ROUTER_PORT),
            capture=True,
        ).stdout.strip()
        if not re.fullmatch(r"127\.0\.0\.1:[1-9]\d{0,4}", binding):
            raise RuntimeError(
                "Compose did not publish exactly one localhost router port"
            )
        base_url = f"http://{binding}"
        log(f"waiting for {base_url}{ProbePath.STARTUP}")
        deadline = time.monotonic() + HEALTH_TIMEOUT_SECONDS
        while self.command(
            "curl",
            "--noproxy",
            "*",
            "--max-time",
            str(HEALTH_POLL_SECONDS),
            "-sf",
            f"{base_url}{ProbePath.STARTUP}",
            capture=True,
            check=False,
        ).returncode:
            if time.monotonic() >= deadline:
                raise RuntimeError(
                    f"router did not finish startup within {HEALTH_TIMEOUT_SECONDS}s"
                )
            time.sleep(HEALTH_POLL_SECONDS)
        for probe in (ProbePath.LIVENESS, ProbePath.CAPACITY):
            if self.command(
                "curl", "--noproxy", "*", "--max-time",
                str(HEALTH_POLL_SECONDS), "-sf", f"{base_url}{probe}",
                capture=True, check=False,
            ).returncode:
                raise RuntimeError(f"router probe {probe} failed after startup")
        self.phase_seconds[SmokePhase.BOOT] = f"{time.monotonic() - started:.1f}"
        started = time.monotonic()
        seed_output = self.command(
            *self.compose, "run", "--rm", ComposeService.SEED, capture=True
        ).stdout
        match = re.search(r"\brk_[A-Za-z0-9_-]+", seed_output)
        if not match:
            raise RuntimeError("seed did not return a router key (output withheld)")
        self.phase_seconds[SmokePhase.SEED] = f"{time.monotonic() - started:.1f}"
        test_environment = self.environment | {
            "SMOKE_ROUTER_KEY": match.group(),
            "SMOKE_BASE_URL": base_url,
            "SMOKE_OPENAI_ENABLED": (
                "1"
                if self.mode == ProxyMode.REPLAY or os.environ.get("OPENAI_API_KEY")
                else "0"
            ),
        }
        for key in MODEL_OVERRIDE_ENVIRONMENTS:
            if key in os.environ:
                test_environment[key] = os.environ[key]
        if self.keep and self.directory:
            test_env = self.directory / "test.env"
            test_env.write_text(
                "\n".join(
                    f"export {key}={shlex.quote(test_environment[key])}"
                    for key in CLIENT_ENVIRONMENTS
                    if key in test_environment
                )
                + "\n"
            )
            test_env.chmod(0o600)
        started = time.monotonic()
        self.command(
            "go",
            "test",
            "-tags",
            "smoke",
            "-count=1",
            "-v",
            "./smoke/",
            environment=test_environment,
            timeout=TEST_TIMEOUT_SECONDS,
        )
        self.phase_seconds[SmokePhase.ASSERTIONS] = f"{time.monotonic() - started:.1f}"
        log("smoke suite passed")

    def cleanup(self) -> None:
        if not self.directory:
            return
        if self.started:
            if self.keep:
                if (self.directory / "test.env").exists():
                    log(
                        f"stack retained; test settings: {self.directory / 'test.env'} (contains local fixture key)"
                    )
                else:
                    log(
                        "stack retained before seeding completed; no test.env was created"
                    )
                log(
                    "inspect this project's logs: "
                    + shlex.join(
                        self.compose
                        + [
                            "logs",
                            "--tail=150",
                            ComposeService.SERVER,
                            ComposeService.MITMPROXY,
                        ]
                    )
                )
                log(
                    "tear down only this project: "
                    + shlex.join(self.compose + ["down", "--volumes"])
                )
                log(f"then remove the retained state directory: {self.directory}")
                return
            self.require_owned_resources()
            self.command(*self.compose, "down", "--volumes")
        if not self.prebuilt:
            for service in BUILT_SERVICES:
                tag = f"{self.project}-{service}"
                identifiers = self.command(
                    "docker",
                    "image",
                    "ls",
                    "--quiet",
                    "--filter",
                    f"reference={tag}",
                    capture=True,
                ).stdout.split()
                if not identifiers:
                    continue
                owner = self.command(
                    "docker",
                    "image",
                    "inspect",
                    "--format",
                    '{{ index .Config.Labels "' + OWNER_LABEL + '" }}',
                    tag,
                    capture=True,
                ).stdout.strip()
                if owner != self.owner:
                    raise RuntimeError(f"refusing cleanup: unowned image tag {tag}")
                self.command("docker", "image", "rm", tag)
        shutil.rmtree(self.directory)

    def summary(self) -> None:
        if summary_path := os.environ.get("GITHUB_STEP_SUMMARY"):
            with open(summary_path, "a") as summary:
                summary.write(
                    "\n## Smoke phase timing\n\n| Phase | Seconds |\n| --- | ---: |\n"
                )
                for phase in SmokePhase:
                    summary.write(
                        f"| {phase} | {self.phase_seconds.get(phase, 'not completed')} |\n"
                    )


def interrupted(*_: object) -> None:
    raise KeyboardInterrupt


def main() -> int:
    smoke: SmokeRun | None = None
    exit_code = 0
    signal.signal(signal.SIGTERM, interrupted)
    try:
        smoke = SmokeRun()
        smoke.run()
    except (
        ValueError,
        RuntimeError,
        OSError,
        subprocess.SubprocessError,
        KeyboardInterrupt,
    ) as error:
        log(f"failed: {error}")
        exit_code = 1
    finally:
        if smoke:
            # Cancellation must not cut short a teardown already in progress.
            # Individual cleanup commands retain their bounded deadlines.
            previous_handlers = {
                signum: signal.signal(signum, signal.SIG_IGN)
                for signum in (signal.SIGINT, signal.SIGTERM)
            }
            try:
                smoke.cleanup()
                smoke.summary()
            except (OSError, RuntimeError, subprocess.SubprocessError) as error:
                log(f"cleanup failed; state retained at {smoke.directory}: {error}")
                exit_code = 1
            finally:
                for signum, previous in previous_handlers.items():
                    signal.signal(signum, previous)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
