import asyncio
import json
import threading

import httpx
import pytest
from fastapi.testclient import TestClient

import app as app_module
from app import create_app, warm_up
from contract import BUDGET_HEADER, MAX_INPUT_BYTES, MAX_INPUT_TOKENS, PROJECTION, SCHEMA

RELEASE = "a" * 64
BEARER = "s" * 32
HEADERS = {"Authorization": "Bearer " + BEARER}
REQUEST = {"schema_version": SCHEMA, "projection_version": PROJECTION,
           "release_sha256": RELEASE, "user_text": "Review the deployment"}


class Predictor:
    def __init__(self, output="0,1,0,1,0", tokens=12):
        self.output = output
        self.tokens = tokens
        self.encoded = 0

    def encode(self, text):
        self.encoded += 1
        return [len(text)] * self.tokens

    async def classify(self, tokens):
        return self.output


class HeldPredictor(Predictor):
    """Keeps every classification in flight until released, recording concurrency and cancellation."""

    def __init__(self):
        super().__init__()
        self.release = asyncio.Event()
        self.active = 0
        self.peak = 0
        self.cancelled = 0

    async def classify(self, tokens):
        self.active += 1
        self.peak = max(self.peak, self.active)
        try:
            await self.release.wait()
            return self.output
        except asyncio.CancelledError:
            self.cancelled += 1
            raise
        finally:
            self.active -= 1


async def wait_until(condition):
    for _ in range(200):
        if condition():
            return
        await asyncio.sleep(0.01)
    raise AssertionError("condition not reached")


def client_for(predictor):
    transport = httpx.ASGITransport(app=create_app(predictor, RELEASE, BEARER))
    return httpx.AsyncClient(transport=transport, base_url="https://classifier.test")


def test_classification_and_auth():
    client = TestClient(create_app(Predictor(), RELEASE, BEARER))
    assert client.post("/classify", json=REQUEST).status_code == 401
    response = client.post("/classify", json=REQUEST, headers=HEADERS)
    assert response.status_code == 200
    assert response.json() == {"schema_version": SCHEMA, "release_sha256": RELEASE,
                               "output": "0,1,0,1,0", "input_tokens": 12}


@pytest.mark.parametrize("changes,status", [
    ({"release_sha256": "b" * 64}, 409),
    ({"schema_version": "unknown"}, 400),
    ({"model": "request-selected-model"}, 400),
    ({"user_text": ""}, 413),
    ({"user_text": "x" * (MAX_INPUT_BYTES + 1)}, 413),
    ({"user_text": "界" * (MAX_INPUT_BYTES // 3 + 1)}, 413),
    ({"user_text": 42}, 400),
])
def test_bad_requests(changes, status):
    client = TestClient(create_app(Predictor(), RELEASE, BEARER))
    assert client.post("/classify", json=REQUEST | changes, headers=HEADERS).status_code == status


@pytest.mark.parametrize("budget", ["0", "-5", "1.5", "abc", "10001", "9" * 5000, b"\xb2", b"\xb9\xb2"])
def test_invalid_budget_header(budget):
    client = TestClient(create_app(Predictor(), RELEASE, BEARER))
    response = client.post("/classify", json=REQUEST, headers=HEADERS | {BUDGET_HEADER: budget})
    assert response.status_code == 400


@pytest.mark.parametrize("output,tokens", [("0,1,0,1,0\n", 12), ("1,0", 12),
                                            ("0,1,0,1,0", 0), ("0,1,0,1,0", MAX_INPUT_TOKENS + 1)])
def test_bad_predictions(output, tokens):
    client = TestClient(create_app(Predictor(output, tokens), RELEASE, BEARER))
    assert client.post("/classify", json=REQUEST, headers=HEADERS).status_code == 503


def test_token_limit_from_encoder_is_rejected():
    class LongPredictor(Predictor):
        def encode(self, text):
            raise ValueError("task input exceeds token budget")

    client = TestClient(create_app(LongPredictor(), RELEASE, BEARER))
    assert client.post("/classify", json=REQUEST, headers=HEADERS).status_code == 413


def test_engine_failure_is_unavailable():
    class FailingPredictor(Predictor):
        async def classify(self, tokens):
            raise RuntimeError("engine dead")

    client = TestClient(create_app(FailingPredictor(), RELEASE, BEARER))
    response = client.post("/classify", json=REQUEST, headers=HEADERS)
    assert response.status_code == 503
    assert response.json()["detail"] == "classifier unavailable"


def test_concurrent_requests_are_all_in_flight_together():
    predictor = HeldPredictor()

    async def scenario():
        async with client_for(predictor) as client:
            requests = [asyncio.create_task(client.post("/classify", json=REQUEST, headers=HEADERS)) for _ in range(12)]
            await wait_until(lambda: predictor.active == 12)
            predictor.release.set()
            return await asyncio.gather(*requests)

    responses = asyncio.run(scenario())
    assert [response.status_code for response in responses] == [200] * 12
    assert predictor.peak == 12


def test_admission_limit_rejects_before_tokenizing_and_frees_slots(monkeypatch):
    monkeypatch.setattr(app_module, "MAX_IN_FLIGHT", 1)
    predictor = HeldPredictor()

    async def scenario():
        async with client_for(predictor) as client:
            held = asyncio.create_task(client.post("/classify", json=REQUEST, headers=HEADERS))
            await wait_until(lambda: predictor.active == 1)
            rejected = await client.post("/classify", json=REQUEST, headers=HEADERS)
            encoded_while_full = predictor.encoded
            predictor.release.set()
            accepted = await held
            after = await client.post("/classify", json=REQUEST, headers=HEADERS)
            return rejected, encoded_while_full, accepted, after

    rejected, encoded_while_full, accepted, after = asyncio.run(scenario())
    assert (rejected.status_code, rejected.json()["detail"]) == (503, "classifier busy")
    assert encoded_while_full == 1
    assert accepted.status_code == 200
    assert after.status_code == 200


def test_deadline_cancels_the_engine_request_and_frees_its_slot(monkeypatch):
    monkeypatch.setattr(app_module, "MAX_IN_FLIGHT", 1)
    predictor = HeldPredictor()

    async def scenario():
        async with client_for(predictor) as client:
            expired_task = asyncio.create_task(client.post("/classify", json=REQUEST, headers=HEADERS | {BUDGET_HEADER: "300"}))
            await wait_until(lambda: predictor.active == 1)
            expired = await expired_task
            cancelled = predictor.cancelled
            predictor.release.set()
            after = await client.post("/classify", json=REQUEST, headers=HEADERS)
            return expired, cancelled, after

    expired, cancelled, after = asyncio.run(scenario())
    assert (expired.status_code, expired.json()["detail"]) == (503, "classification deadline exceeded")
    assert cancelled == 1
    assert after.status_code == 200


class SlowEncoder(HeldPredictor):
    """Tokenization blocks until released; classification is recorded if it is ever reached."""

    def __init__(self):
        super().__init__()
        self.encode_release = threading.Event()
        self.classified = 0

    def encode(self, text):
        assert self.encode_release.wait(5)
        return super().encode(text)

    async def classify(self, tokens):
        self.classified += 1
        return self.output


def test_deadline_covers_tokenization_and_frees_the_slot(monkeypatch):
    monkeypatch.setattr(app_module, "MAX_IN_FLIGHT", 1)
    predictor = SlowEncoder()

    async def scenario():
        async with client_for(predictor) as client:
            expired = await client.post("/classify", json=REQUEST, headers=HEADERS | {BUDGET_HEADER: "100"})
            predictor.encode_release.set()
            after = await client.post("/classify", json=REQUEST, headers=HEADERS)
            return expired, after

    expired, after = asyncio.run(scenario())
    assert (expired.status_code, expired.json()["detail"]) == (503, "classification deadline exceeded")
    assert after.status_code == 200
    assert predictor.classified == 1


def test_tokenizer_failure_is_unavailable_not_a_server_error():
    class BrokenTokenizer(Predictor):
        def encode(self, text):
            raise RuntimeError("tokenizer crashed")

    client = TestClient(create_app(BrokenTokenizer(), RELEASE, BEARER))
    response = client.post("/classify", json=REQUEST, headers=HEADERS)
    assert (response.status_code, response.json()["detail"]) == (503, "classifier unavailable")


def test_disconnected_caller_cancels_the_engine_request():
    predictor = HeldPredictor()
    app = create_app(predictor, RELEASE, BEARER)

    async def scenario():
        messages = [{"type": "http.request", "body": json.dumps(REQUEST).encode(), "more_body": False}]
        hung_up = asyncio.Event()

        async def receive():
            if messages:
                return messages.pop(0)
            await hung_up.wait()
            return {"type": "http.disconnect"}

        sent = []

        async def send(message):
            sent.append(message)

        headers = [(b"authorization", HEADERS["Authorization"].encode()), (b"content-type", b"application/json")]
        scope = {"type": "http", "asgi": {"version": "3.0"}, "http_version": "1.1", "method": "POST",
                 "scheme": "https", "path": "/classify", "raw_path": b"/classify", "query_string": b"",
                 "root_path": "", "headers": headers, "client": ("caller", 1), "server": ("classifier.test", 443)}
        abandoned = asyncio.create_task(app(scope, receive, send))
        await wait_until(lambda: predictor.active == 1)
        hung_up.set()
        await abandoned
        return sent

    sent = asyncio.run(scenario())
    assert sent[0]["status"] == 503
    assert predictor.cancelled == 1


def test_warm_up_requires_valid_repeat_pass_alone_and_concurrent():
    class ColdPredictor(Predictor):
        def __init__(self, repeat_output):
            super().__init__()
            self.calls = []
            self.repeat_output = repeat_output

        def encode(self, text):
            return [ord(text[0])]

        async def classify(self, tokens):
            self.calls.append(tokens[0])
            return "1" if len(self.calls) <= 4 else self.repeat_output

    warmed = ColdPredictor("0,1,0,1,0")
    asyncio.run(warm_up(warmed, ("short", "long")))
    short, long = ord("s"), ord("l")
    assert warmed.calls == [short, long, short, long] * 2
    with pytest.raises(RuntimeError, match="invalid output for warmup input 0"):
        asyncio.run(warm_up(ColdPredictor("1"), ("short", "long")))


def test_warm_up_propagates_inference_errors():
    class BrokenPredictor(Predictor):
        async def classify(self, tokens):
            raise RuntimeError("Failed to find C compiler")

    with pytest.raises(RuntimeError, match="C compiler"):
        asyncio.run(warm_up(BrokenPredictor(), ("short",)))


def test_weak_bearer_is_rejected():
    with pytest.raises(ValueError, match="strong bearer"):
        create_app(Predictor(), RELEASE, "short")
