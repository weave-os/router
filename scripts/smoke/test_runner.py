"""Exercise the real entrypoint with inert executables; never start Docker."""

from __future__ import annotations

from enum import StrEnum
import importlib.util
import inspect
import json
import os
from pathlib import Path
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SOURCE_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SOURCE_ROOT / "scripts"))
from agent_checks import (  # noqa: E402 - discovery needs scripts/ on sys.path
    ProxyMode,
)


class FakeFailure(StrEnum):
    GO_TIMEOUT = "go-timeout"
    GO = "go"
    SIGNAL = "signal"
    UP = "up"
    SEED = "seed"
    DOWN = "down"
    BUILD = "build"
    CANCEL_CLEANUP = "cancel-cleanup"


FAKE_TOOL = r"""#!/usr/bin/env python3
from enum import StrEnum
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

# FAILURE_ENUM

tool = Path(sys.argv[0]).name
args = sys.argv[1:]
root = Path(os.environ["FAKE_STATE"])
entry = {"tool": tool, "args": args, "credentials": {
    key: value for key, value in os.environ.items()
    if key.endswith(("_API_KEY", "_TOKEN")) or key.startswith("SMOKE_RECORD_")
}, "test_url": os.environ.get("SMOKE_BASE_URL"),
    "openai_enabled": os.environ.get("SMOKE_OPENAI_ENABLED"),
    "go_flags": os.environ.get("GOFLAGS"),
    "go_cache": os.environ.get("GOCACHE"),
    "pin_model": os.environ.get("SMOKE_PIN_MODEL"),
    "test_database": os.environ.get("ROUTER_TEST_DATABASE_URL"),
    "live_sidecar": os.environ.get("ROUTER_POLICY_LIVE_TEST_URL")}
if tool == "docker" and args[:1] == ["compose"] and args[1:2] != ["version"]:
    project = (args[args.index("--project-name") + 1] if "--project-name" in args
               else os.environ.get("COMPOSE_PROJECT_NAME", Path.cwd().name))
    entry["project"] = project
    override = next(Path(args[index + 1]) for index, value in enumerate(args)
                    if value == "-f" and args[index + 1].endswith(("isolation.yml", "smoke-run.override.yml")))
    entry["override"] = override.read_text()
    entry["override_path"] = str(override)
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps(entry) + "\n")
failure = os.environ.get("FAKE_FAIL")
if tool == "go":
    if failure == FakeFailure.GO_TIMEOUT:
        subprocess.Popen([sys.executable, "-c",
            "import signal, time; from pathlib import Path; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(1); Path(" + repr(str(root / "orphan-marker")) + ").touch()"])
        time.sleep(10)
    sys.exit(4 if failure == FakeFailure.GO else 0)
if tool == "curl":
    sys.exit(0)
if args[:3] == ["compose", "version", "--short"]:
    print(os.environ.get("FAKE_COMPOSE_VERSION", "2.35.1"))
elif args[1:2] == ["ls"]:
    project = args[-1].split("=", 2)[-1]
    if (root / project).exists() or os.environ.get("FAKE_COLLISION"):
        print(project)
elif args[:2] == ["image", "inspect"]:
    print("unowned" if os.environ.get("FAKE_UNOWNED_IMAGE") else (root / args[-1]).read_text())
elif args[:2] == ["image", "rm"]:
    (root / args[-1]).unlink()
elif args[1:2] == ["inspect"]:
    print("unowned" if os.environ.get("FAKE_UNOWNED") else args[-1].removeprefix("router-smoke-"))
elif "build" in args:
    for service in args[args.index("build") + 1:]:
        (root / (project + "-" + service)).write_text(project.removeprefix("router-smoke-"))
    if failure == FakeFailure.BUILD:
        sys.exit(7)
elif "up" in args:
    (root / project).touch()
    if failure == FakeFailure.SIGNAL:
        os.kill(os.getppid(), signal.SIGTERM)
    if failure == FakeFailure.UP:
        sys.exit(5)
elif "port" in args:
    print("127.0.0.1:49170")
elif "run" in args:
    if failure != FakeFailure.SEED:
        print("fixture rk_test_synthetic")
elif "down" in args:
    if failure == FakeFailure.DOWN:
        sys.exit(6)
    if failure == FakeFailure.CANCEL_CLEANUP:
        os.kill(os.getppid(), signal.SIGINT)
        time.sleep(0.05)
        os.kill(os.getppid(), signal.SIGTERM)
        time.sleep(0.05)
    (root / project).unlink(missing_ok=True)
""".replace("# FAILURE_ENUM", inspect.getsource(FakeFailure))


class SmokeRunnerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="smoke-runner-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo with spaces"
        for relative in (
            "scripts/smoke/run.sh",
            "scripts/smoke/runner.py",
            "scripts/agent_checks.py",
            "docker-compose.yml",
            "smoke/mitmproxy/docker-compose.yml",
            "smoke/mitmproxy/docker-compose.ci-cache.yml",
        ):
            target = self.repo / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(SOURCE_ROOT / relative, target)
        (self.repo / ".env.local").write_text(
            "ANTHROPIC_API_KEY=private-local-synthetic\n"
        )
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("docker", "go", "curl"):
            executable = self.bin / name
            executable.write_text(FAKE_TOOL)
            executable.chmod(0o755)
        self.state = self.root / "state"
        self.state.mkdir()
        (self.state / "developer-stack").write_text("unrelated developer resources")
        self.environment = {
            key: value
            for key, value in os.environ.items()
            if not key.startswith(("SMOKE_", "FAKE_", "COMPOSE_"))
            and not key.endswith("_API_KEY")
        } | {
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "FAKE_STATE": str(self.state),
            "TMPDIR": str(self.root),
            "ANTHROPIC_API_KEY": "synthetic-host-anthropic-secret",
            "OPENAI_API_KEY": "synthetic-host-openai-secret",
            "GOOGLE_API_KEY": "synthetic-host-google-secret",
            "COMPOSE_PROJECT_NAME": "developer-stack",
            "COMPOSE_PROFILES": "hmm",
            "PYTHONDONTWRITEBYTECODE": "1",
        }

    def run_smoke(self, **environment: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", str(self.repo / "scripts/smoke/run.sh")],
            cwd=self.repo,
            env=self.environment | environment,
            text=True,
            capture_output=True,
            timeout=20,
        )

    def calls(self) -> list[dict]:
        path = self.state / "calls.jsonl"
        return (
            [json.loads(line) for line in path.read_text().splitlines()]
            if path.exists()
            else []
        )

    def test_replay_owns_project_port_environment_and_cleanup(self) -> None:
        completed = self.run_smoke()
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        calls = self.calls()
        compose_calls = [call for call in calls if "project" in call]
        projects = {call["project"] for call in compose_calls}
        self.assertEqual(len(projects), 1)
        self.assertRegex(next(iter(projects)), r"^router-smoke-[a-f0-9]{32}$")
        self.assertTrue(all(call["credentials"] == {} for call in calls))
        for call in compose_calls:
            self.assertIn("--env-file", call["args"])
            self.assertEqual(
                call["args"][call["args"].index("--env-file") + 1], os.devnull
            )
            self.assertNotIn("synthetic-host", call["override"])
            self.assertNotIn("private-local", call["override"])
            self.assertIn('ports: !override ["127.0.0.1::8080"]', call["override"])
            self.assertIn("internal: true", call["override"])
            self.assertIn("/cassettes:ro", call["override"])
            self.assertEqual(call["override"].count("env_file: !reset []"), 2)
            self.assertEqual(call["override"].count("ports: !reset []"), 3)
        self.assertEqual(
            [call["args"][-2:] for call in compose_calls if "down" in call["args"]],
            [["down", "--volumes"]],
        )
        go_call = next(call for call in calls if call["tool"] == "go")
        self.assertEqual(go_call["test_url"], "http://127.0.0.1:49170")
        self.assertEqual(go_call["openai_enabled"], "1")
        self.assertFalse(Path(compose_calls[0]["override_path"]).parent.exists())
        self.assertEqual(
            (self.state / "developer-stack").read_text(),
            "unrelated developer resources",
        )
        self.assertEqual(
            (self.repo / ".env.local").read_text(),
            "ANTHROPIC_API_KEY=private-local-synthetic\n",
        )

    def test_separate_invocations_never_share_projects(self) -> None:
        self.assertEqual(self.run_smoke().returncode, 0)
        self.assertEqual(self.run_smoke().returncode, 0)
        projects = {call["project"] for call in self.calls() if "project" in call}
        self.assertEqual(len(projects), 2)

    def test_cancellation_during_teardown_finishes_resource_and_directory_cleanup(
        self,
    ) -> None:
        completed = self.run_smoke(FAKE_FAIL=FakeFailure.CANCEL_CLEANUP.value)
        self.assertEqual(0, completed.returncode, completed.stdout + completed.stderr)
        calls = self.calls()
        teardown = next(call for call in calls if "down" in call["args"])
        self.assertFalse(Path(teardown["override_path"]).parent.exists())
        self.assertEqual(3, sum(call["args"][:2] == ["image", "rm"] for call in calls))
        self.assertFalse(list(self.state.glob("router-smoke-*")))
        self.assertEqual(
            "unrelated developer resources",
            (self.state / "developer-stack").read_text(),
        )

    def test_direct_runner_strips_ambient_flags_credentials_and_live_database(
        self,
    ) -> None:
        completed = self.run_smoke(
            GOFLAGS="-run=NoTests -tags=google_integration",
            GH_TOKEN="synthetic-host-token",
            ROUTER_TEST_DATABASE_URL="postgres://synthetic.invalid/shared",
            ROUTER_POLICY_LIVE_TEST_URL="https://synthetic.invalid/policy",
            SMOKE_PIN_MODEL="synthetic-fixture-pin",
        )
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        for call in self.calls():
            self.assertEqual(call["go_flags"], "-mod=readonly")
            self.assertEqual(call["go_cache"], os.environ.get("GOCACHE"))
            self.assertEqual(call["credentials"], {})
            self.assertIsNone(call["test_database"])
            self.assertIsNone(call["live_sidecar"])
        self.assertEqual(
            next(call for call in self.calls() if call["tool"] == "go")["pin_model"],
            "synthetic-fixture-pin",
        )

    def test_command_timeout_terminates_descendants_then_cleans_owned_stack(
        self,
    ) -> None:
        entrypoint = self.repo / "scripts/smoke/runner.py"
        program = (
            "import runpy; "
            f"scope = runpy.run_path({str(entrypoint)!r}); "
            "scope['main'].__globals__['TEST_TIMEOUT_SECONDS'] = 0.2; "
            "raise SystemExit(scope['main']())"
        )
        completed = subprocess.run(
            [sys.executable, "-c", program],
            cwd=self.repo,
            env=self.environment | {"FAKE_FAIL": FakeFailure.GO_TIMEOUT.value},
            text=True,
            capture_output=True,
            timeout=20,
        )
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("timed out", completed.stdout)
        self.assertTrue(any("down" in call["args"] for call in self.calls()))
        time.sleep(1.1)
        self.assertFalse((self.state / "orphan-marker").exists())
        self.assertEqual(
            (self.state / "developer-stack").read_text(),
            "unrelated developer resources",
        )

    def test_ci_cache_token_is_only_available_to_explicit_image_build(self) -> None:
        completed = self.run_smoke(
            SMOKE_CI_CACHE="1", ACTIONS_RUNTIME_TOKEN="synthetic-cache-token"
        )
        self.assertEqual(completed.returncode, 0, completed.stdout)
        for call in self.calls():
            expected = (
                {"ACTIONS_RUNTIME_TOKEN": "synthetic-cache-token"}
                if "build" in call["args"]
                else {}
            )
            self.assertEqual(call["credentials"], expected)

    def test_failed_boot_assertions_and_seed_still_cleanup(self) -> None:
        for failure in (
            FakeFailure.UP,
            FakeFailure.GO,
            FakeFailure.SEED,
            FakeFailure.SIGNAL,
        ):
            with self.subTest(failure=failure):
                before = len(self.calls())
                completed = self.run_smoke(FAKE_FAIL=failure.value)
                self.assertNotEqual(completed.returncode, 0)
                self.assertTrue(
                    any("down" in call["args"] for call in self.calls()[before:])
                )

    def test_prebuilt_images_are_reused_without_overwriting_build_tags(self) -> None:
        completed = self.run_smoke(SMOKE_PREBUILT="1")
        self.assertEqual(completed.returncode, 0, completed.stdout)
        self.assertFalse(any("build" in call["args"] for call in self.calls()))
        self.assertFalse(
            any(call["args"][:2] == ["image", "rm"] for call in self.calls())
        )
        override = next(call["override"] for call in self.calls() if "project" in call)
        for image in ("router-server", "router-seed", "router-mitmproxy"):
            self.assertIn(f"image: {image}\n", override)

    def test_owned_image_tags_are_removed_even_after_partial_build_failure(
        self,
    ) -> None:
        for failure in ("", FakeFailure.BUILD.value):
            with self.subTest(failure=failure):
                before = len(self.calls())
                completed = self.run_smoke(FAKE_FAIL=failure)
                self.assertEqual(
                    completed.returncode == 0, not failure, completed.stdout
                )
                image_removals = [
                    call
                    for call in self.calls()[before:]
                    if call["args"][:2] == ["image", "rm"]
                ]
                self.assertEqual(3, len(image_removals))
                self.assertFalse(list(self.state.glob("router-smoke-*")))

    def test_unowned_image_tag_is_never_removed(self) -> None:
        completed = self.run_smoke(FAKE_UNOWNED_IMAGE="1")
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("unowned image tag", completed.stdout)
        self.assertFalse(
            any(call["args"][:2] == ["image", "rm"] for call in self.calls())
        )

    def test_cleanup_failure_is_not_reported_as_success(self) -> None:
        completed = self.run_smoke(FAKE_FAIL=FakeFailure.DOWN.value)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("cleanup failed; state retained", completed.stdout)
        override = next(
            call["override_path"] for call in self.calls() if "project" in call
        )
        self.assertTrue(Path(override).exists())

    def test_foreign_owner_blocks_destructive_cleanup(self) -> None:
        completed = self.run_smoke(FAKE_UNOWNED="1")
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("refusing cleanup", completed.stdout)
        self.assertFalse(any("down" in call["args"] for call in self.calls()))

    def test_collision_blocks_build_boot_and_cleanup(self) -> None:
        completed = self.run_smoke(FAKE_COLLISION="1")
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("refusing to reuse", completed.stdout)
        self.assertFalse(any("project" in call for call in self.calls()))

    def test_unsupported_compose_blocks_before_any_resource_operation(self) -> None:
        completed = self.run_smoke(FAKE_COMPOSE_VERSION="2.23.0")
        self.assertNotEqual(completed.returncode, 0)
        self.assertEqual(len(self.calls()), 1)

    def test_keep_stack_retains_valid_quoted_teardown_and_test_settings(self) -> None:
        completed = self.run_smoke(SMOKE_KEEP_STACK="1")
        self.assertEqual(completed.returncode, 0, completed.stdout)
        self.assertFalse(any("down" in call["args"] for call in self.calls()))
        command = next(
            line.split("project: ", 1)[1]
            for line in completed.stdout.splitlines()
            if "tear down only this project:" in line
        )
        arguments = shlex.split(command)
        retained_files = [
            Path(arguments[index + 1])
            for index, value in enumerate(arguments)
            if value == "-f"
        ]
        self.assertTrue(all(path.exists() for path in retained_files))
        test_env = retained_files[-1].parent / "test.env"
        self.assertIn("SMOKE_BASE_URL=http://127.0.0.1:49170", test_env.read_text())
        self.assertEqual(test_env.stat().st_mode & 0o777, 0o600)
        teardown = subprocess.run(
            arguments, env=self.environment, text=True, capture_output=True
        )
        self.assertEqual(teardown.returncode, 0, teardown.stderr)

    def test_existing_base_url_is_rejected_without_touching_stack(self) -> None:
        completed = self.run_smoke(SMOKE_BASE_URL="https://prod.invalid")
        self.assertNotEqual(completed.returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_record_requires_explicit_key_and_only_passes_record_credentials(
        self,
    ) -> None:
        missing = self.run_smoke(
            SMOKE_PROXY_MODE=ProxyMode.RECORD.value, ANTHROPIC_API_KEY=""
        )
        self.assertNotEqual(missing.returncode, 0)
        self.assertEqual(self.calls(), [])
        completed = self.run_smoke(
            SMOKE_PROXY_MODE=ProxyMode.RECORD.value, OPENAI_API_KEY=""
        )
        self.assertEqual(completed.returncode, 0, completed.stdout)
        up = next(call for call in self.calls() if "up" in call["args"])
        self.assertEqual(
            up["credentials"]["SMOKE_RECORD_ANTHROPIC_KEY"],
            "synthetic-host-anthropic-secret",
        )
        self.assertNotIn("synthetic-host-anthropic-secret", up["override"])
        self.assertIn("internal: false", up["override"])
        self.assertIn("/cassettes:rw", up["override"])
        go_call = next(call for call in self.calls() if call["tool"] == "go")
        self.assertEqual(go_call["openai_enabled"], "0")
        self.assertEqual(go_call["credentials"], {})


class ComposeMergeTest(unittest.TestCase):
    def test_real_compose_config_never_reads_local_env_or_exposes_dependency_ports(
        self,
    ) -> None:
        docker = shutil.which("docker")
        if not docker:
            self.skipTest(
                "Docker CLI unavailable; no daemon is required for config verification"
            )
        probe = subprocess.run(
            [docker, "compose", "version", "--short"], capture_output=True, text=True
        )
        if probe.returncode:
            self.skipTest("Compose unavailable")
        spec = importlib.util.spec_from_file_location(
            "smoke_runner", SOURCE_ROOT / "scripts/smoke/runner.py"
        )
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        with tempfile.TemporaryDirectory(prefix="smoke-compose-merge-") as temporary:
            root = Path(temporary)
            # Invalid syntax makes accidental env-file loading fail loudly.
            (root / ".env.local").write_text("=invalid local environment\n")
            (root / ".env").write_text("=invalid implicit environment\n")
            for source, target in (
                ("docker-compose.yml", "base.yml"),
                ("smoke/mitmproxy/docker-compose.yml", "proxy.yml"),
            ):
                shutil.copy2(SOURCE_ROOT / source, root / target)
            with patch.dict(
                os.environ,
                {"SMOKE_PROXY_MODE": ProxyMode.REPLAY.value, "SMOKE_BASE_URL": ""},
            ):
                smoke = runner.SmokeRun()
            (root / "isolation.yml").write_text(smoke.isolation_config())
            command = [
                docker,
                "compose",
                "--project-directory",
                str(root),
                "--env-file",
                os.devnull,
                "-p",
                "smoke-config-test",
                "-f",
                str(root / "base.yml"),
                "-f",
                str(root / "proxy.yml"),
                "-f",
                str(root / "isolation.yml"),
                "config",
                "--format",
                "json",
            ]
            rendered = subprocess.run(
                command, capture_output=True, text=True, env=smoke.environment
            )
            self.assertEqual(rendered.returncode, 0, rendered.stderr)
            config = json.loads(rendered.stdout)
            server = config["services"]["server"]
            self.assertNotIn("env_file", server)
            self.assertEqual(
                server["environment"]["ANTHROPIC_API_KEY"], runner.PLACEHOLDER_KEY
            )
            self.assertNotIn("ports", server)
            ingress = config["services"]["smoke-ingress"]
            self.assertEqual(ingress["ports"][0]["host_ip"], "127.0.0.1")
            self.assertNotIn("published", ingress["ports"][0])
            self.assertEqual(set(server["networks"]), {"default"})
            self.assertEqual(
                set(config["services"]["mitmproxy"]["networks"]), {"default"}
            )
            self.assertEqual(set(ingress["networks"]), {"default", "ingress"})
            self.assertEqual(ingress["entrypoint"][-2:], ["server", "8080"])
            self.assertEqual(ingress["cap_drop"], ["ALL"])
            self.assertNotIn("ports", config["services"]["postgres"])
            self.assertNotIn("ports", config["services"]["pubsub-emulator"])
            self.assertTrue(config["networks"]["default"]["internal"])
            cassette = next(
                mount
                for mount in config["services"]["mitmproxy"]["volumes"]
                if mount["target"] == "/cassettes"
            )
            self.assertTrue(cassette["read_only"])

    @unittest.skipUnless(
        os.environ.get("SMOKE_TEST_DOCKER") == "1",
        "explicit disposable Docker network probe",
    )
    def test_replay_ingress_reaches_internal_server_without_default_route(self) -> None:
        spec = importlib.util.spec_from_file_location(
            "smoke_runner", SOURCE_ROOT / "scripts/smoke/runner.py"
        )
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        with patch.dict(
            os.environ,
            {
                "SMOKE_PROXY_MODE": ProxyMode.REPLAY.value,
                "SMOKE_BASE_URL": "",
                "SMOKE_PREBUILT": "1",
                "SMOKE_KEEP_STACK": "",
            },
        ):
            smoke = runner.SmokeRun()
        try:
            smoke.prepare()
            config = json.loads(
                smoke.command(
                    *smoke.compose, "config", "--format", "json", capture=True
                ).stdout
            )
            fixture = {
                "services": {
                    "server": {
                        "image": "postgres:15-alpine",
                        "entrypoint": [
                            "/bin/busybox",
                            "nc",
                            "-lk",
                            "-p",
                            "8080",
                            "-e",
                            "/bin/busybox",
                            "cat",
                        ],
                        "labels": {runner.OWNER_LABEL: smoke.owner},
                        "networks": ["default"],
                    },
                    "smoke-ingress": config["services"]["smoke-ingress"],
                },
                "networks": config["networks"],
            }
            path = smoke.directory / "network-probe.json"
            path.write_text(json.dumps(fixture))
            smoke.compose = [
                "docker",
                "compose",
                "--project-name",
                smoke.project,
                "-f",
                str(path),
            ]
            smoke.started = True
            smoke.command(*smoke.compose, "up", "-d")
            binding = smoke.command(
                *smoke.compose, "port", "smoke-ingress", "8080", capture=True
            ).stdout.strip()
            host, port = binding.split(":")
            self.assertEqual(host, "127.0.0.1")
            # `up -d` only starts the containers; both listeners must become ready.
            deadline = time.monotonic() + runner.HEALTH_TIMEOUT_SECONDS
            while True:
                try:
                    with socket.create_connection(
                        (host, int(port)), timeout=1
                    ) as connection:
                        connection.sendall(b"ready")
                        if connection.recv(128) == b"ready":
                            break
                except OSError:
                    pass
                if time.monotonic() >= deadline:
                    self.fail("disposable network probe did not become ready")
                time.sleep(0.1)
            for _ in range(2):
                with socket.create_connection(
                    (host, int(port)), timeout=5
                ) as connection:
                    connection.sendall(b"synthetic-network-probe")
                    self.assertEqual(connection.recv(128), b"synthetic-network-probe")
            routes = smoke.command(
                *smoke.compose,
                "exec",
                "-T",
                "server",
                "/bin/busybox",
                "cat",
                "/proc/net/route",
                capture=True,
            ).stdout
            self.assertNotIn(
                "00000000", [line.split()[1] for line in routes.splitlines()[1:]]
            )
        finally:
            smoke.cleanup()


if __name__ == "__main__":
    unittest.main()
