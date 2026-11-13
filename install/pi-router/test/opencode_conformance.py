#!/usr/bin/env python3
"""Exercise the plugin-free OpenCode v2 Responses integration locally."""

import json
import os
import re
import shutil
import signal
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path

import mock_router
from responses_fixture import TEXT

INSTALL = Path(__file__).resolve().parents[2]
SUPPORTED_VERSION = "2.0.18"
COMMAND_TIMEOUT = 60


def stop_process_group(process: subprocess.Popen) -> None:
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=5)


class OpenCodeV2Conformance(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.work = Path(tempfile.mkdtemp(prefix="weave-opencode-v2-"))
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
        })
        cls.opencode = shutil.which(os.environ.get("OPENCODE_BIN", "opencode"))
        if not cls.opencode:
            raise RuntimeError("OpenCode v2 must be installed before running the conformance suite")
        version_output = cls.command([cls.opencode, "--version"]).stdout.strip()
        match = re.search(r"(?<!\d)(\d+\.\d+\.\d+(?:[-+][\w.-]+)?)", version_output)
        if not match:
            raise AssertionError(f"could not parse OpenCode version: {version_output!r}")
        version = match.group(1)
        expected = os.environ.get("OPENCODE_EXPECTED_VERSION", SUPPORTED_VERSION)
        if version != expected or version.split(".", 1)[0] != "2":
            raise AssertionError(f"OpenCode {version} != supported v2 version {expected}")
        print(f"OpenCode v2 conformance: {version}", flush=True)

        mock_router.LOG_PATH = str(cls.work / "requests.jsonl")
        Path(mock_router.LOG_PATH).touch()
        cls.server = mock_router.ThreadingHTTPServer(("127.0.0.1", 0), mock_router.Handler)
        cls.addClassCleanup(cls.server.server_close)
        cls.addClassCleanup(cls.server.shutdown)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base_url = f"http://127.0.0.1:{cls.server.server_port}"
        cls.command([
            "bash", str(INSTALL / "install.sh"), "--opencode", "--non-interactive", "--quiet",
            "--dir", str(cls.work), "--base-url", cls.base_url,
        ])
        cls.config_path = cls.work / "opencode.json"
        cls.config = json.loads(cls.config_path.read_text())

    @classmethod
    def command(cls, args: list[str]) -> subprocess.CompletedProcess:
        process = subprocess.Popen(
            args, cwd=getattr(cls, "work", Path.cwd()), env=getattr(cls, "env", os.environ.copy()),
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, start_new_session=True,
        )
        try:
            stdout, stderr = process.communicate(timeout=COMMAND_TIMEOUT)
        except subprocess.TimeoutExpired as error:
            stop_process_group(process)
            process.communicate(timeout=5)
            raise AssertionError(f"command timed out after {COMMAND_TIMEOUT}s: {args[:3]}") from error
        finally:
            stop_process_group(process)
        if process.returncode != 0:
            raise AssertionError(f"command exited {process.returncode}: {stdout}\n{stderr}")
        return subprocess.CompletedProcess(args, process.returncode, stdout, stderr)

    def requests(self) -> list[dict]:
        records = (json.loads(line) for line in Path(mock_router.LOG_PATH).read_text().splitlines())
        return [record for record in records if record["method"] == "POST"]

    def run_turn(self, prompt: str, *flags: str) -> tuple[list[dict], list[dict]]:
        before = len(self.requests())
        completed = self.command([
            self.opencode, "run", "--standalone", "--format", "json", *flags, prompt,
        ])
        events = [json.loads(line) for line in completed.stdout.splitlines() if line.startswith("{")]
        requests = [request for request in self.requests()[before:] if request["path"] == "/v1/responses"]
        self.assertTrue(events, completed.stdout + completed.stderr)
        self.assertTrue(requests, completed.stdout + completed.stderr)
        for request in requests:
            self.assertEqual(request["app"], "opencode")
            self.assertEqual(request["model"], "auto")
            self.assertTrue(request["stream"])
            self.assertTrue(request["key_present"])
            self.assertEqual(request["key_suffix"], "_key")
            self.assertTrue(request["session_id"])
            user_agent = request["user_agent"]
            self.assertIn("/2.", user_agent)
            self.assertFalse(request["weave_agent"])
        return events, requests

    def assert_finished(self, events: list[dict]) -> None:
        self.assertFalse([event for event in events if event["type"] == "error"], events)
        self.assertEqual([event["part"]["text"] for event in events if event["type"] == "text"], [TEXT])
        # step_finish is not reliably flushed to stdout before `run` exits on Linux.
        exported = self.command([self.opencode, "session", "export", "--standalone", events[0]["sessionID"]]).stdout
        messages = json.loads(exported[exported.index("{"):])["messages"]
        assistant = [message for message in messages if message["type"] == "assistant"]
        self.assertTrue(assistant, messages)
        self.assertEqual(assistant[-1]["finish"], "stop")
        self.assertEqual(assistant[-1]["tokens"], {
            "input": 9, "output": 6, "reasoning": 2,
            "cache": {"read": 3, "write": 0},
        })

    def test_installed_config_has_no_plugin(self) -> None:
        self.assertEqual(self.config["model"], "weave/auto")
        provider = self.config["provider"]["weave"]
        self.assertEqual(provider["npm"], "@ai-sdk/openai")
        self.assertEqual(provider["options"]["baseURL"], self.base_url + "/v1")
        self.assertNotIn("plugin", self.config)
        self.assertFalse((self.work / ".weave/opencode-weave.ts").exists())

    def test_responses_and_native_session_continuity(self) -> None:
        events, requests = self.run_turn("Return the test marker")
        self.assert_finished(events)
        session = events[0]["sessionID"]
        self.assertEqual({request["session_id"] for request in requests}, {session})
        title_requests = [request for request in requests if request["instructions"].startswith("You are a title generator")]
        self.assertTrue(title_requests, requests)
        self.assertFalse(title_requests[0]["weave_agent"])

        resumed, requests = self.run_turn("Continue this session", "--session", session)
        self.assertFalse([event for event in resumed if event["type"] == "error"], resumed)
        self.assertEqual([event["part"]["text"] for event in resumed if event["type"] == "text"], [TEXT])
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0]["session_id"], session)
        assistant_text = [
            block.get("text")
            for item in requests[0]["input"] if item.get("role") == "assistant"
            for block in item.get("content", [])
        ]
        self.assertIn(TEXT, assistant_text)

    def test_title_request_uses_native_payload_only(self) -> None:
        _, requests = self.run_turn("Return the title marker")
        titles = [request for request in requests if request["instructions"].startswith("You are a title generator")]
        self.assertTrue(titles, requests)
        self.assertNotIn("tools", titles[0]["body_keys"])
        self.assertNotIn("x-weave-opencode-agent", titles[0]["header_names"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
