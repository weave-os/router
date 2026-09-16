#!/usr/bin/env python3
"""Drive the installed OpenCode integration with localhost-only model responses."""

import json
import os
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import unittest
from pathlib import Path
from urllib.error import URLError
from urllib.request import urlopen

import mock_router
from responses_fixture import Scenario, TEXT

INSTALL = Path(__file__).resolve().parents[2]
PINNED_VERSION = json.loads((INSTALL / "opencode-weave/package.json").read_text())["devDependencies"]["@opencode-ai/plugin"]
COMMAND_TIMEOUT = 60


def stop_process_group(process: subprocess.Popen) -> None:
    # Signal the whole session even after the leader exited: a same-group helper
    # (OpenCode's local server) can outlive a successful `opencode run`.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=5)


class OpenCodeConformance(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.work = Path(tempfile.mkdtemp(prefix="weave-opencode-"))
        cls.addClassCleanup(shutil.rmtree, cls.work)
        cls.env = {key: os.environ[key] for key in ("PATH", "SYSTEMROOT", "TMPDIR") if key in os.environ}
        for key, directory in {
            "HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
            "XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state",
        }.items():
            cls.env[key] = str(cls.work / directory)
            (cls.work / directory).mkdir()
        cls.env.update({
            "WEAVE_ROUTER_KEY": "rk_oc_smoke_key", "WEAVE_USER_EMAIL": "test@example.test",
            "WEAVE_USER_NAME": "Conformance test", "NO_COLOR": "1", "CI": "true",
            "OPENCODE_DISABLE_AUTOUPDATE": "true", "OPENCODE_DISABLE_MODELS_FETCH": "true",
            "OPENCODE_DISABLE_DEFAULT_PLUGINS": "true",
        })
        cls.opencode = shutil.which(os.environ.get("OPENCODE_BIN", "opencode"))
        if not cls.opencode:
            raise RuntimeError("opencode must be installed before running the conformance suite")
        version = cls.command([cls.opencode, "--version"]).stdout.strip()
        expected = os.environ.get("OPENCODE_EXPECTED_VERSION", PINNED_VERSION)
        if version != expected:
            raise AssertionError(f"OpenCode {version} != supported {expected}; use the pinned CLI or explicitly set OPENCODE_EXPECTED_VERSION")
        print(f"OpenCode conformance: {version}", flush=True)
        mock_router.LOG_PATH = str(cls.work / "requests.jsonl")
        Path(mock_router.LOG_PATH).touch()
        cls.server = mock_router.ThreadingHTTPServer(("127.0.0.1", 0), mock_router.Handler)
        cls.addClassCleanup(cls.server.server_close)
        cls.addClassCleanup(cls.server.shutdown)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base_url = f"http://127.0.0.1:{cls.server.server_port}"
        cls.command(["bash", str(INSTALL / "install.sh"), "--opencode", "--non-interactive", "--quiet",
                     "--dir", str(cls.work), "--base-url", cls.base_url])
        cls.config_path = cls.work / "opencode.json"
        cls.config = json.loads(cls.config_path.read_text())
        # Keep installer output intact except explicit test isolation/observation.
        observer = cls.work / "lifecycle-plugin.ts"
        shutil.copyfile(INSTALL / "opencode-weave/test/fixtures/lifecycle-plugin.ts", observer)
        cls.config["plugin"].append(str(observer))
        cls.config.update({"enabled_providers": ["weave", "weave-claude"], "small_model": "weave/auto",
                           "permission": {"*": "deny", "bash": "allow", "task": "allow"}})
        cls.config_path.write_text(json.dumps(cls.config))

    @classmethod
    def command(cls, args: list[str], scenario: Scenario = Scenario.TEXT, expected_exit: int = 0) -> subprocess.CompletedProcess:
        process = subprocess.Popen(args, cwd=cls.work, env={**cls.env, "OPENCODE_CONFORMANCE_SCENARIO": scenario.value},
                                   stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   text=True, start_new_session=True)
        try:
            stdout, stderr = process.communicate(timeout=COMMAND_TIMEOUT)
        except subprocess.TimeoutExpired as error:
            stop_process_group(process)
            process.communicate(timeout=5)
            raise AssertionError(f"command timed out after {COMMAND_TIMEOUT}s: {args[:3]}") from error
        finally:
            stop_process_group(process)
        if process.returncode != expected_exit:
            raise AssertionError(f"command exited {process.returncode}: {stdout}\n{stderr}")
        return subprocess.CompletedProcess(args, process.returncode, stdout, stderr)

    def requests(self) -> list[dict]:
        records = (json.loads(line) for line in Path(mock_router.LOG_PATH).read_text().splitlines())
        return [record for record in records if record["method"] == "POST"]

    def run_turn(self, scenario: Scenario = Scenario.TEXT, *flags: str) -> tuple[list[dict], list[dict]]:
        before = len(self.requests())
        completed = self.command([self.opencode, "run", "--format", "json", *flags, "Return the test marker"],
                                 scenario, expected_exit=1 if scenario == Scenario.ERROR else 0)
        events = [json.loads(line) for line in completed.stdout.splitlines() if line.startswith("{")]
        requests = self.requests()[before:]
        self.assertTrue(events, completed.stdout + completed.stderr)
        self.assertTrue(requests, completed.stdout + completed.stderr)
        for request in requests:
            self.assertEqual(request["path"], "/v1/responses", request)
            self.assertFalse(request["rejected"])
            self.assertEqual(request["app"], "opencode")
            self.assertEqual(request["model"], "auto")
            self.assertTrue(request["stream"])
            self.assertTrue(request["key_present"])
            self.assertEqual(request["key_suffix"], "_key")
            self.assertTrue(request["session_id"])
        return events, requests

    def assert_finished(self, events: list[dict]) -> None:
        self.assertFalse([event for event in events if event["type"] == "error"], events)
        self.assertEqual([event["part"]["text"] for event in events if event["type"] == "text"], [TEXT])
        finishes = [event["part"] for event in events if event["type"] == "step_finish"]
        self.assertTrue(finishes, events)
        self.assertEqual(finishes[-1]["reason"], "stop")
        self.assertEqual(finishes[-1]["tokens"], {
            "total": 20, "input": 9, "output": 6, "reasoning": 2,
            "cache": {"read": 3, "write": 0},
        })

    def test_installed_config(self) -> None:
        self.assertEqual(self.config["model"], "weave/auto")
        provider = self.config["provider"]["weave"]
        self.assertEqual(provider["npm"], "@ai-sdk/openai")
        self.assertEqual(provider["options"]["baseURL"], self.base_url + "/v1")
        self.assertTrue((self.work / ".weave/opencode-weave.ts").is_file())

    def test_main_title_and_session_continuity(self) -> None:
        events, requests = self.run_turn()
        self.assert_finished(events)
        self.assertEqual({request["agent"] for request in requests}, {"build", "title"})
        session = events[0]["sessionID"]
        self.assertEqual({request["session_id"] for request in requests}, {session})
        events, requests = self.run_turn(Scenario.TEXT, "--session", session)
        self.assert_finished(events)
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0]["session_id"], session)
        assistant = [item for item in requests[0]["input"] if item.get("role") == "assistant"]
        self.assertEqual(assistant, [{"role": "assistant", "content": [{"type": "output_text", "text": TEXT}]}])

    def test_multiple_tool_calls(self) -> None:
        events, requests = self.run_turn(Scenario.TOOLS, "--title", "Tool contract")
        self.assert_finished(events)
        self.assertEqual(len(requests), 2)
        self.assertEqual(requests[0]["session_id"], requests[1]["session_id"])
        outputs = {item["call_id"]: item["output"].strip() for item in requests[1]["tool_outputs"]}
        self.assertEqual(outputs, {"call_mock_0": "TOOL_0_OK", "call_mock_1": "TOOL_1_OK"})
        tool_events = [event["part"] for event in events if event["type"] == "tool_use"]
        self.assertEqual({part["callID"] for part in tool_events}, set(outputs))
        for part in tool_events:
            self.assertEqual(part["state"]["status"], "completed")
        types = [event["type"] for event in events]
        self.assertLess(types.index("step_start"), types.index("tool_use"))
        self.assertLess(types.index("tool_use"), types.index("text"))
        self.assertEqual(types[-1], "step_finish")

    def test_subagent(self) -> None:
        events, requests = self.run_turn(Scenario.TASK, "--title", "Task contract")
        self.assert_finished(events)
        self.assertEqual([request["agent"] for request in requests], ["build", "explore", "build"])
        self.assertEqual(requests[0]["session_id"], requests[2]["session_id"])
        self.assertNotEqual(requests[0]["session_id"], requests[1]["session_id"])
        self.assertEqual(requests[2]["tool_outputs"][0]["call_id"], "call_task")
        self.assertIn(TEXT, requests[2]["tool_outputs"][0]["output"])

    def test_compaction(self) -> None:
        events, requests = self.run_turn(Scenario.COMPACTION, "--title", "Compaction contract")
        self.assertEqual([request["agent"] for request in requests], ["build", "compaction", "build"])
        self.assertEqual(len({request["session_id"] for request in requests}), 1)
        self.assertFalse([event for event in events if event["type"] == "error"])
        self.assertEqual(events[-1]["type"], "step_finish")
        self.assertEqual(events[-1]["part"]["reason"], "stop")
        self.assertEqual(events[-1]["part"]["tokens"]["input"], 9)
        self.assertEqual([event["part"]["text"] for event in events if event["type"] == "text"][-1], TEXT)
        resumed, requests = self.run_turn(Scenario.TEXT, "--session", events[0]["sessionID"])
        self.assert_finished(resumed)
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0]["session_id"], events[0]["sessionID"])

    def test_upstream_error(self) -> None:
        events, requests = self.run_turn(Scenario.ERROR, "--title", "Error contract")
        self.assertEqual(len(requests), 1)
        self.assertTrue([event for event in events if event["type"] == "error"], events)
        self.assertIn("MOCK_REJECTED", json.dumps(events))
        self.assertFalse([event for event in events if event["type"] == "text"])

    def test_auth_hooks_loaded_by_real_cli(self) -> None:
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        with (self.work / "serve.log").open("w") as log:
            process = subprocess.Popen([self.opencode, "serve", "--hostname", "127.0.0.1", "--port", str(port)],
                                       cwd=self.work, env=self.env, stdin=subprocess.DEVNULL,
                                       stdout=log, stderr=log, start_new_session=True)
            try:
                deadline = time.monotonic() + COMMAND_TIMEOUT
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        self.fail("OpenCode server exited: " + (self.work / "serve.log").read_text())
                    try:
                        with urlopen(f"http://127.0.0.1:{port}/provider/auth", timeout=5) as response:
                            methods = json.load(response)
                        break
                    except (URLError, TimeoutError):
                        time.sleep(0.1)
                else:
                    self.fail("OpenCode auth API never became ready")
                self.assertEqual(len(methods["weave"]), 2)
                self.assertEqual(len(methods["weave-claude"]), 1)
                self.assertTrue(all(method["type"] == "oauth" for provider in ("weave", "weave-claude")
                                    for method in methods[provider]))
            finally:
                stop_process_group(process)


if __name__ == "__main__":
    unittest.main(verbosity=2)
