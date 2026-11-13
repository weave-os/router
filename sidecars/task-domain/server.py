"""Serve an operator-staged checkpoint; never download or publish model artifacts."""

from __future__ import annotations

import asyncio
import os
import signal
import uuid
from collections.abc import Sequence
from pathlib import Path
from typing import Final

import uvicorn
from transformers import AutoTokenizer, PreTrainedTokenizerBase
from vllm import SamplingParams
from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.inputs import TokensPrompt
from vllm.v1.engine.async_llm import AsyncLLM

from app import create_app, require_strong_bearer, warm_up
from contract import MAX_INPUT_TOKENS, SYSTEM_PROMPT, verify_release

WARMUP_TEXTS: Final = (
    "Fix the failing unit test in the payment service.",
    "Add a dark mode toggle to the settings page and persist the choice.",
    "Write a migration that backfills the region column from the address table.",
    "Explain why this deployment configuration fails.\n" + "service: example\nreplicas: 3\n" * 600,
)
MAX_NEW_TOKENS: Final = 16
# Concurrent sequences the engine decodes together; admitted requests beyond this wait in its queue.
MAX_NUM_SEQS: Final = 64


class QwenPredictor:
    def __init__(self, tokenizer: PreTrainedTokenizerBase, engine: AsyncLLM) -> None:
        self.tokenizer = tokenizer
        self.engine = engine
        self.sampling = SamplingParams(temperature=0.0, max_tokens=MAX_NEW_TOKENS, skip_special_tokens=True)

    def encode(self, text: str) -> list[int]:
        prompt: str = self.tokenizer.apply_chat_template(
            [{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": text}],
            tokenize=False, add_generation_prompt=True, enable_thinking=False,
        )
        tokens: list[int] = self.tokenizer.encode(prompt, add_special_tokens=False)
        if not 1 <= len(tokens) <= MAX_INPUT_TOKENS:
            raise ValueError("task input exceeds token budget")
        return tokens

    async def classify(self, tokens: Sequence[int]) -> str:
        # Cancelling this coroutine (deadline or disconnect) aborts the request inside the engine.
        text: str = ""
        async for output in self.engine.generate(TokensPrompt(prompt_token_ids=list(tokens)), self.sampling, uuid.uuid4().hex):
            text = output.outputs[0].text
        return text.strip()


def exit_on_sigterm(signum: int, frame: object) -> None:
    raise SystemExit(128 + signum)


async def serve(predictor: QwenPredictor, release_sha256: str, bearer: str) -> None:
    app = create_app(predictor, release_sha256, bearer)
    await warm_up(predictor, WARMUP_TEXTS)
    server = uvicorn.Server(uvicorn.Config(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8095")), access_log=False))
    await server.serve()


if __name__ == "__main__":
    model_path: Path = Path(os.environ["TASK_DOMAIN_MODEL_PATH"])
    release_sha256: str = verify_release(Path(os.environ["TASK_DOMAIN_RELEASE_PATH"]), model_path, os.environ["TASK_DOMAIN_RELEASE_SHA256"])
    bearer: str = os.environ["TASK_DOMAIN_BEARER"]
    # Engine startup takes minutes; reject a bad secret before spending them.
    require_strong_bearer(bearer)
    # A SIGTERM while the engine core is starting is deferred until construction returns,
    # so the shutdown below always owns the core process that holds the GPU.
    deferred_signals: list[int] = []
    signal.signal(signal.SIGTERM, lambda signum, frame: deferred_signals.append(signum))
    # Build the engine here in module scope, outside any function and before the event loop
    # starts. Built inside a function (or inside the running loop) the same engine served
    # concurrent requests about 2x slower on an L4 with vLLM 0.31.0, reproducibly.
    tokenizer = AutoTokenizer.from_pretrained(model_path, local_files_only=True, trust_remote_code=False)
    engine = AsyncLLM.from_engine_args(AsyncEngineArgs(
        model=str(model_path), tokenizer=str(model_path), trust_remote_code=False, dtype="bfloat16",
        max_model_len=MAX_INPUT_TOKENS + MAX_NEW_TOKENS, max_num_seqs=MAX_NUM_SEQS,
        gpu_memory_utilization=0.85, seed=0,
    ))
    try:
        # uvicorn re-raises SIGTERM after its graceful shutdown; with the default handler that
        # kills the process before the engine shutdown below can run.
        signal.signal(signal.SIGTERM, exit_on_sigterm)
        if deferred_signals:
            raise SystemExit(128 + deferred_signals[0])
        asyncio.run(serve(QwenPredictor(tokenizer, engine), release_sha256, bearer))
    finally:
        # The engine core is a child process holding the GPU; without this it outlives
        # a terminated server and the next start cannot allocate GPU memory.
        engine.shutdown()
