#!/usr/bin/env python3
"""Plan incident validation without loading .env files or contacting providers."""

from __future__ import annotations

import argparse
import fnmatch
import json
import os
import shutil
import signal
import subprocess
import sys
from dataclasses import asdict, dataclass
from enum import StrEnum
from pathlib import Path


class SuiteID(StrEnum):
    DOCS = "docs"
    TOOLING = "tooling"
    GO = "go"
    LINT = "lint"
    INSTALL = "install"
    INSTALL_CLI = "install-cli"
    HMM = "hmm"
    TASK_DOMAIN = "task-domain"
    FRONTEND = "frontend"
    DATABASE = "database"
    SMOKE = "smoke"


class Outcome(StrEnum):
    PASSED = "passed"
    FAILED = "failed"
    BLOCKED = "blocked"


class PlanClassification(StrEnum):
    ALL_REQUIRED = "all-required"
    CHANGED_PATHS = "changed-paths"


class Operation(StrEnum):
    DOCTOR = "doctor"
    PLAN = "plan"
    RUN = "run"
    SELECTED = "selected"


class ProxyMode(StrEnum):
    REPLAY = "replay-only"
    RECORD = "record"
    REPLAY_OR_RECORD = "replay-or-record"


PROXY_MODE_ENV = "SMOKE_PROXY_MODE"
SUITE_TIMEOUT_SECONDS = 1200
SHUTDOWN_GRACE_SECONDS = 10


def run_command(
    command, *, timeout: float | None, cleanup_owner: bool = False, **options
) -> subprocess.CompletedProcess:
    """Own the command's process group until completion, timeout or interruption."""

    def interrupt(signum, frame):
        raise KeyboardInterrupt

    previous_terminate = signal.getsignal(signal.SIGTERM)
    # Teardown may deliberately ignore cancellation while finishing owned work.
    if previous_terminate != signal.SIG_IGN:
        signal.signal(signal.SIGTERM, interrupt)
    try:
        child = subprocess.Popen(command, start_new_session=True, **options)
        try:
            stdout, stderr = child.communicate(timeout=timeout)
        except (subprocess.TimeoutExpired, KeyboardInterrupt):
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            if cleanup_owner:
                # Smoke owns Docker teardown and has bounded command deadlines.
                # Do not kill it mid-cleanup, even if cancellation is repeated.
                while True:
                    try:
                        child.communicate()
                        break
                    except KeyboardInterrupt:
                        continue
            else:
                try:
                    child.communicate(timeout=SHUTDOWN_GRACE_SECONDS)
                except (subprocess.TimeoutExpired, KeyboardInterrupt):
                    pass
                finally:
                    # A parent may exit while a descendant ignores SIGTERM.
                    # Stop the group even if communicate() already returned.
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    child.communicate(timeout=SHUTDOWN_GRACE_SECONDS)
            raise
        return subprocess.CompletedProcess(command, child.returncode, stdout, stderr)
    finally:
        signal.signal(signal.SIGTERM, previous_terminate)


@dataclass(frozen=True)
class Suite:
    id: SuiteID
    patterns: tuple[str, ...]
    commands: tuple[tuple[str, ...], ...]
    prerequisites: tuple[str, ...]
    integration: bool = False
    note: str = ""


# CI's smoke gate and the local planner consume this same request-path closure.
SMOKE_PATHS = (
    "internal/*",
    "cmd/router/*",
    "db/*",
    "smoke/*",
    "scripts/smoke/*",
    "scripts/agent_checks.py",
    "docker-compose*",
    "Dockerfile*",
    "docker-bake.smoke.hcl",
    "go.mod",
    "go.sum",
    "Makefile",
    ".github/workflows/*",
)
INSTALL_PATHS = ("install/*", "internal/router/catalog/*", "Makefile", ".github/*")
SUITES = (
    Suite(
        SuiteID.DOCS,
        ("*",),
        (
            ("python3", "scripts/generate_agent_guides.py", "--check"),
            ("python3", "scripts/check_docs.py"),
        ),
        ("python3", "git"),
    ),
    Suite(
        SuiteID.TOOLING,
        ("scripts/*", ".agents/*", ".claude/*", "Makefile", ".github/*"),
        (
            (
                "python3",
                "-m",
                "unittest",
                "discover",
                "-s",
                "scripts",
                "-p",
                "test_*.py",
            ),
            (
                "python3",
                "-m",
                "unittest",
                "discover",
                "-s",
                "scripts/smoke",
                "-p",
                "test_*.py",
            ),
        ),
        ("python3", "bash", "make", "git"),
    ),
    Suite(
        SuiteID.GO,
        (
            "*.go",
            "internal/*",
            "cmd/*",
            "db/*",
            "go.*",
            "scripts/*",
            "Makefile",
            "Dockerfile*",
            ".github/*",
            "install/cc-statusline.sh",
            "install/install.sh",
            "install/pi-router/src/pricing.generated.ts",
            "bench/weave_bench/prices.generated.json",
        ),
        (
            ("go", "run", "./cmd/geninferencepolicy", "--check"),
            ("go", "test", "-count=1", "./..."),
            ("go", "vet", "./..."),
        ),
        ("go",),
        note="Native tokenizer libraries may be required; never change or clear GOCACHE to retry.",
    ),
    Suite(
        SuiteID.LINT,
        ("*.go", "internal/*", "cmd/*", "go.*", ".golangci*", "Makefile", ".github/*"),
        (("golangci-lint", "run"),),
        ("golangci-lint",),
    ),
    Suite(
        SuiteID.INSTALL,
        INSTALL_PATHS,
        (
            (
                "python3",
                "-m",
                "unittest",
                "discover",
                "-s",
                "install/pi-router/test",
                "-p",
                "test_*.py",
            ),
        ),
        ("bash", "node", "jq", "python3", "curl"),
        note="Run the offline install/tests/*_test.sh scripts and pi-router Python tests.",
    ),
    Suite(
        SuiteID.INSTALL_CLI,
        INSTALL_PATHS,
        (("bash", "install/pi-router/test/opencode_smoke.sh"),),
        ("bash", "python3", "opencode"),
        integration=True,
        note="OpenCode conformance uses disposable localhost fixtures and enforces the driver's pinned CLI version.",
    ),
    Suite(
        SuiteID.TASK_DOMAIN,
        ("sidecars/task-domain/*", "internal/router/taskdomain/*", "internal/policyclient/task_domain*", ".github/*"),
        (("uv", "run", "--no-env-file", "--project", "sidecars/task-domain", "--locked", "--extra", "test", "pytest", "-q", "sidecars/task-domain/tests"),),
        ("uv",),
        note="Synthetic predictor tests; no GPU, model downloads or provider calls.",
    ),
    Suite(
        SuiteID.HMM,
        (
            "sidecars/hmm/*",
            "internal/policyclient/*",
            "internal/router/hmm/*",
            "cmd/router/hmm*",
            "Dockerfile*",
            "Makefile",
            ".github/*",
        ),
        (
            (
                "uv",
                "run",
                "--no-env-file",
                "--project",
                "sidecars/hmm",
                "--locked",
                "pytest",
                "-q",
                "sidecars/hmm/tests",
            ),
        ),
        ("uv",),
        integration=True,
        note="Also requires CI's downloaded-artifact verification, policy contract and sidecar image checks.",
    ),
    Suite(
        SuiteID.FRONTEND,
        ("frontend/*", "assets/ui/types/*", "Dockerfile*", ".github/*"),
        (),
        ("node",),
        integration=True,
        note="Run the frontend build/type checks from the pinned package manifest and CI workflow.",
    ),
    Suite(
        SuiteID.DATABASE,
        (
            "db/*",
            "internal/postgres/*",
            "internal/policyregistry/*",
            "cmd/*",
            "go.*",
            ".github/*",
        ),
        (),
        ("docker", "psql", "migrate", "sqlc"),
        integration=True,
        note="CI migration roundtrip, SQLC drift and DB-backed serving checks require a disposable local database; never use a shared DB.",
    ),
    Suite(
        SuiteID.SMOKE,
        SMOKE_PATHS,
        (("bash", "scripts/smoke/run.sh"),),
        ("docker", "go", "curl", "python3"),
        integration=True,
        note="Isolated replay-only stack; image/dependency downloads, no inference-provider calls.",
    ),
)


def changed_paths(root: Path, base: str, head: str | None = None) -> list[str] | None:
    """None means unclassifiable and selects every suite, never an empty success."""
    if not base.strip() or (head is not None and not head.strip()):
        return None
    commands = [
        [
            "git",
            "diff",
            "--name-only",
            "-z",
            "--no-renames",
            f"{base}...{head or 'HEAD'}",
            "--",
        ]
    ]
    if head is None:
        commands.extend(
            [
                ["git", "diff", "--name-only", "-z", "--no-renames", "HEAD", "--"],
                ["git", "ls-files", "--others", "--exclude-standard", "-z"],
            ]
        )
    paths: set[str] = set()
    for command in commands:
        try:
            proc = subprocess.run(command, cwd=root, capture_output=True, timeout=30)
        except (OSError, subprocess.TimeoutExpired):
            return None
        if proc.returncode:
            return None
        paths.update(os.fsdecode(path) for path in proc.stdout.split(b"\0") if path)
    return sorted(paths)


def select_suites(paths: list[str] | None) -> list[Suite]:
    return [
        suite
        for suite in SUITES
        if paths is None
        or any(
            fnmatch.fnmatchcase(path, pattern)
            for path in paths
            for pattern in suite.patterns
        )
    ]


def plan(paths: list[str] | None) -> dict:
    return {
        "schema_version": 1,
        "classification": (
            PlanClassification.ALL_REQUIRED
            if paths is None
            else PlanClassification.CHANGED_PATHS
        ),
        "paths": paths,
        "suites": [asdict(suite) for suite in select_suites(paths)],
        "scope": "Local validation plan, not proof of complete CI or production correctness.",
    }


def doctor(root: Path) -> dict:
    tools = sorted({tool for suite in SUITES for tool in suite.prerequisites})
    return {
        "schema_version": 1,
        "tools": {tool: shutil.which(tool) is not None for tool in tools},
        "files": {
            name: (root / name).exists()
            for name in (
                "go.mod",
                "internal/sqlc",
                "smoke/mitmproxy/cassettes",
                "sidecars/hmm/uv.lock",
            )
        },
        "not_checked": [
            "tool versions",
            "Docker daemon",
            "cloud authentication",
            "native libraries",
            "live services",
            "provider access",
        ],
        "note": "Read-only discovery; no env files loaded, credentials printed, dependencies installed, or caches changed.",
    }


def validation_environment(suite: SuiteID) -> dict[str, str]:
    """Ambient credentials and opt-in integration knobs are not test authority."""
    runtime_prefixes = (
        "ROUTER_",
        "HMM_",
        "ESCALATION_",
        "SMOKE_",
        "WEAVE_",
        "ANTHROPIC_",
        "OPENAI_",
        "OPENCODE_",
        "GOOGLE_",
        "GEMINI_",
        "OPENROUTER_",
        "FIREWORKS_",
        "DEEPINFRA_",
        "MAKORA_",
        "TOGETHER_",
        "MINIMAX_",
        "XAI_",
        "META_",
        "WAFER_",
        "AWS_",
        "AZURE_",
        "GCP_",
        "CLOUDSDK_",
    )
    credential_suffixes = (
        "_API_KEY",
        "_AUTH_TOKEN",
        "_ACCESS_TOKEN",
        "_TOKEN",
        "_PASSWORD",
        "_SECRET",
        "_PRIVATE_KEY",
        "_CREDENTIALS",
        "_CREDENTIALS_JSON",
        "_DSN",
        "_DATABASE_URL",
        "_TEST_URL",
    )
    excluded = {
        "GOFLAGS",
        "BASH_ENV",
        "ENV",
        "NODE_OPTIONS",
        "PYTHONPATH",
        "PYTEST_ADDOPTS",
        "PYTEST_PLUGINS",
        "INSTALLER",
        "UNINSTALLER",
        "UV_ENV_FILE",
        "MAKEFLAGS",
        "MFLAGS",
        "MAKEFILES",
        "DATABASE_URL",
        "DATABASE_URL_READ",
        "PGHOST",
        "PGPORT",
        "PGUSER",
        "PGPASSWORD",
        "PGDATABASE",
        "PGSERVICE",
        "PGSERVICEFILE",
        "PGPASSFILE",
        "API_KEY",
        "TOKEN",
        "PASSWORD",
        "SECRET",
    }
    native_dependencies = {"ROUTER_ONNX_ASSETS_DIR", "ROUTER_ONNX_LIBRARY_DIR"}
    # Copy untouched tool/cache configuration, including any inherited Go cache.
    # Never assign, clear, or redirect it to obtain a passing validation.
    environment = {
        key: value
        for key, value in os.environ.items()
        if key in native_dependencies
        or (
            key not in excluded
            and not key.startswith(runtime_prefixes)
            and not key.endswith(credential_suffixes)
        )
    }
    # A nonempty approved value also overrides persistent `go env -w GOFLAGS`,
    # which could otherwise skip assertions or enable live integration tags.
    environment["GOFLAGS"] = "-mod=readonly"
    if suite == SuiteID.SMOKE:
        environment[PROXY_MODE_ENV] = ProxyMode.REPLAY.value
    return environment


def run_suite(root: Path, suite: Suite, integration: bool) -> dict:
    missing = [tool for tool in suite.prerequisites if shutil.which(tool) is None]
    receipt: dict = {"suite": suite.id, "outcome": Outcome.BLOCKED, "commands": []}
    if missing:
        receipt["reason"] = "Missing tools: " + ", ".join(missing)
        return receipt
    if suite.integration and not integration:
        receipt["reason"] = (
            "Integration not authorized; re-run with --integration for local fixtures/downloads. "
            + suite.note
        )
        return receipt
    if not suite.commands:
        receipt["reason"] = suite.note
        return receipt
    commands = suite.commands
    if suite.id == SuiteID.INSTALL:
        commands = (
            tuple(
                ("bash", str(path.relative_to(root)))
                for path in sorted((root / "install/tests").glob("*_test.sh"))
            )
            + suite.commands
        )
    for command in commands:
        # Go/uv may fetch dependencies; no suite adopts a shell's test database,
        # live sidecar, provider keys, alternate installer or recording mode.
        command_env = validation_environment(suite.id)
        try:
            # The smoke owner handles its own deadlines and stack cleanup.
            proc = run_command(
                command,
                cwd=root,
                env=command_env,
                stdout=sys.stderr,
                stderr=sys.stderr,
                timeout=None if suite.id == SuiteID.SMOKE else SUITE_TIMEOUT_SECONDS,
                cleanup_owner=suite.id == SuiteID.SMOKE,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            receipt["reason"] = type(error).__name__
            return receipt
        receipt["commands"].append(
            {"argv": list(command), "exit_code": proc.returncode}
        )
        if proc.returncode:
            receipt["outcome"] = Outcome.FAILED
            return receipt
    # HMM's local pytest is useful but does not replace release-artifact/contract CI.
    if suite.id == SuiteID.HMM:
        receipt["reason"] = suite.note
    else:
        receipt["outcome"] = Outcome.PASSED
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", type=Operation, choices=list(Operation))
    parser.add_argument("suite", nargs="?", type=SuiteID, choices=list(SuiteID))
    parser.add_argument("--base", default="origin/main")
    parser.add_argument("--head")
    parser.add_argument("--paths", nargs="*")
    parser.add_argument("--integration", action="store_true")
    args = parser.parse_args()
    if args.suite is not None and args.operation != Operation.SELECTED:
        parser.error(
            "The suite argument is only valid for selected; use --paths to plan/run changed components"
        )
    root = Path(__file__).resolve().parents[1]
    if args.operation == Operation.DOCTOR:
        print(json.dumps(doctor(root), indent=2))
        return 0
    paths = (
        args.paths
        if args.paths is not None
        else changed_paths(root, args.base, args.head)
    )
    selected = select_suites(paths)
    if args.operation == Operation.SELECTED:
        if args.suite is None:
            parser.error("selected requires a suite")
        print(str(any(suite.id == args.suite for suite in selected)).lower())
        return 0
    report = plan(paths)
    if args.operation == Operation.RUN:
        report["receipts"] = [
            run_suite(root, suite, args.integration) for suite in selected
        ]
    print(json.dumps(report, indent=2))
    return int(
        any(
            receipt["outcome"] != Outcome.PASSED
            for receipt in report.get("receipts", [])
        )
    )


if __name__ == "__main__":
    raise SystemExit(main())
