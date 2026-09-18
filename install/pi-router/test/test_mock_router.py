"""HTTP guards for the shared mock, including the existing Pi contract."""

import json
import tempfile
import threading
import unittest
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import mock_router


class MockRouterContract(unittest.TestCase):
    def setUp(self) -> None:
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        mock_router.LOG_PATH = str(Path(work.name) / "requests.jsonl")
        server = mock_router.ThreadingHTTPServer(("127.0.0.1", 0), mock_router.Handler)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{server.server_port}"

    def post(self, path: str, body: dict, app: str = "pi") -> tuple[str, dict]:
        request = Request(self.url + path, json.dumps(body).encode(),
                          headers={"Content-Type": "application/json", "X-App": app})
        with urlopen(request, timeout=3) as response:
            return response.read().decode(), dict(response.headers)

    def test_pi_messages_json_and_sse(self) -> None:
        for stream in (False, True):
            with self.subTest(stream=stream):
                payload, headers = self.post("/v1/messages", {
                    "model": "auto", "stream": stream,
                    "messages": [{"role": "user", "content": "Hello"}],
                })
                self.assertEqual(headers["x-router-model"], mock_router.MAIN_MODEL)
                if stream:
                    events = [json.loads(line[6:]) for line in payload.splitlines() if line.startswith("data: ")]
                    self.assertEqual([event["type"] for event in events], [
                        "message_start", "content_block_start", "content_block_delta",
                        "content_block_stop", "message_delta", "message_stop",
                    ])
                    self.assertEqual(events[2]["delta"]["text"], "MAIN_LOOP_OK")
                else:
                    self.assertEqual(json.loads(payload)["content"][0]["text"], "MAIN_LOOP_OK")

    def test_pi_dispatch_continuation_and_handoff(self) -> None:
        prompt = {"role": "user", "content": "__DISPATCH__"}
        payload, _ = self.post("/v1/messages", {"messages": [prompt]})
        call = json.loads(payload)["content"][0]
        self.assertEqual(call["type"], "tool_use")
        self.assertEqual(call["name"], "dispatch")
        self.assertEqual(len(call["input"]["tasks"]), 2)
        payload, headers = self.post("/v1/messages", {"messages": [prompt]}, "pi-subagent")
        self.assertEqual(json.loads(payload)["content"][0]["text"], "SUBAGENT_OK")
        self.assertEqual(headers["x-router-model"], mock_router.SUBAGENT_MODEL)
        payload, _ = self.post("/v1/messages", {"messages": [prompt, {
            "role": "user", "content": [{"type": "tool_result", "tool_use_id": call["id"], "content": "done"}],
        }]})
        self.assertEqual(json.loads(payload)["content"][0]["text"], "DISPATCH_COMPLETE_OK")
        payload, _ = self.post("/v1/route/handoff", {})
        self.assertEqual(json.loads(payload), {"bypass": True})

    def test_responses_full_event_order_and_no_invented_app(self) -> None:
        payload, _ = self.post("/v1/responses", {"model": "auto", "stream": True}, app="")
        events = [json.loads(line[6:]) for line in payload.splitlines() if line.startswith("data: ")]
        self.assertEqual([event["type"] for event in events], [
            "response.created", "response.in_progress", "response.output_item.added",
            "response.content_part.added", "response.output_text.delta", "response.output_text.delta",
            "response.output_text.done", "response.content_part.done", "response.output_item.done",
            "response.completed",
        ])
        self.assertEqual([event["sequence_number"] for event in events], list(range(10)))
        self.assertEqual(events[-1]["response"]["usage"]["total_tokens"], 20)
        records = [json.loads(line) for line in Path(mock_router.LOG_PATH).read_text().splitlines()]
        self.assertEqual(records[-1]["app"], "")

    def test_invalid_responses_paths_rejected(self) -> None:
        for path in ("/responses", "/v1/v1/responses", "/v1/chat/completions"):
            with self.subTest(path=path), self.assertRaises(HTTPError) as error:
                self.post(path, {}, app="opencode")
            self.assertEqual(error.exception.code, 404)
            error.exception.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
