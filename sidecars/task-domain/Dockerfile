FROM ghcr.io/astral-sh/uv:0.9.28 AS uv
FROM python:3.11-slim
COPY --from=uv /uv /usr/local/bin/uv
# vLLM's Triton kernels (Qwen3.5 linear attention, torch.compile) build small C helpers at startup.
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY sidecars/task-domain/pyproject.toml sidecars/task-domain/uv.lock ./
RUN uv sync --locked --extra qwen --no-dev
COPY sidecars/task-domain/app.py sidecars/task-domain/contract.py sidecars/task-domain/server.py ./
# Greedy decoding never uses FlashInfer's sampler, whose JIT build would need the full CUDA
# toolkit. Usage-stat reporting is disabled because this service handles task text.
ENV PYTHONUNBUFFERED=1 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 VLLM_USE_FLASHINFER_SAMPLER=0 \
    VLLM_NO_USAGE_STATS=1 DO_NOT_TRACK=1
EXPOSE 8095
CMD ["/app/.venv/bin/python", "server.py"]
