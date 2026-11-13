"""Synthetic Responses wire fixtures; no provider calls or client fingerprints."""

import json
from enum import Enum


class Scenario(str, Enum):
    TEXT = "text"
    TOOLS = "tools"
    TASK = "task"
    COMPACTION = "compaction"
    MISSING_USAGE = "missing_usage"
    ERROR = "error"
    MALFORMED = "malformed"
    TRUNCATED = "truncated"
    UNSUPPORTED = "unsupported"
    CODEX_CHILD = "codex_child"


TEXT = "OPENCODE_OK"
USAGE = {
    "input_tokens": 12,
    "output_tokens": 8,
    "total_tokens": 20,
    "input_tokens_details": {"cached_tokens": 3},
    "output_tokens_details": {"reasoning_tokens": 2},
}


def responses_fixture(
    scenario: Scenario, agent: str, has_tool_output: bool, child_agent_id: str = "", waited_for_child: bool = False,
) -> tuple[dict, list[tuple[str, dict]]]:
    items: list[dict] = []
    if scenario == Scenario.CODEX_CHILD and child_agent_id and not waited_for_child:
        items.append({
            "id": "fc_codex_wait", "type": "function_call", "status": "completed",
            "call_id": "call_codex_wait", "name": "wait_agent", "namespace": "multi_agent_v1",
            "arguments": json.dumps({"targets": [child_agent_id], "timeout_ms": 30_000}),
        })
    elif scenario == Scenario.CODEX_CHILD and not has_tool_output:
        items.append({
            "id": "fc_codex_child", "type": "function_call", "status": "completed",
            "call_id": "call_codex_child", "name": "spawn_agent", "namespace": "multi_agent_v1",
            "arguments": json.dumps({"message": "Return only CHILD_OK."}),
        })
    elif agent == "build" and not has_tool_output:
        if scenario == Scenario.TOOLS:
            for index in range(2):
                items.append({
                    "id": f"fc_mock_{index}", "type": "function_call",
                    "status": "completed", "call_id": f"call_mock_{index}",
                    "name": "bash", "arguments": json.dumps({
                        "command": f"printf TOOL_{index}_OK", "description": "Print test marker",
                    }),
                })
        elif scenario == Scenario.TASK:
            items.append({
                "id": "fc_task", "type": "function_call", "status": "completed",
                "call_id": "call_task", "name": "task", "arguments": json.dumps({
                    "description": "Test child session", "prompt": "Return the test marker",
                    "subagent_type": "explore",
                }),
            })
    if not items:
        items.append({
            "id": "msg_mock", "type": "message", "status": "completed", "role": "assistant",
            "content": [{"type": "output_text", "text": TEXT, "annotations": []}],
        })
    response: dict = {
        "id": "resp_mock", "object": "response", "created_at": 1,
        "status": "completed", "model": "mock", "output": items,
        "error": None, "incomplete_details": None,
    }
    if scenario != Scenario.MISSING_USAGE:
        response["usage"] = USAGE
    if scenario == Scenario.COMPACTION and agent == "build":
        response["usage"] = {**USAGE, "input_tokens": 127_500, "total_tokens": 127_508}

    events: list[tuple[str, dict]] = []

    def emit(event: str, **fields: object) -> None:
        events.append((event, {"type": event, "sequence_number": len(events), **fields}))

    pending = {**response, "status": "in_progress", "output": [], "usage": None}
    emit("response.created", response=pending)
    emit("response.in_progress", response=pending)
    for index, item in enumerate(items):
        ref = {"output_index": index, "item_id": item["id"]}
        if item["type"] == "function_call":
            emit("response.output_item.added", output_index=index,
                 item={**item, "status": "in_progress", "arguments": ""})
            arguments = item["arguments"]
            midpoint = len(arguments) // 2
            for delta in (arguments[:midpoint], arguments[midpoint:]):
                emit("response.function_call_arguments.delta", **ref, delta=delta)
            emit("response.function_call_arguments.done", **ref, arguments=arguments)
        else:
            emit("response.output_item.added", output_index=index,
                 item={**item, "status": "in_progress", "content": []})
            emit("response.content_part.added", **ref, content_index=0,
                 part={"type": "output_text", "text": "", "annotations": []})
            for delta in (TEXT[:4], TEXT[4:]):
                emit("response.output_text.delta", **ref, content_index=0, delta=delta)
            emit("response.output_text.done", **ref, content_index=0, text=TEXT)
            emit("response.content_part.done", **ref, content_index=0, part=item["content"][0])
        emit("response.output_item.done", output_index=index, item=item)
    emit("response.completed", response=response)
    if scenario == Scenario.TRUNCATED:
        events = events[:2]
    elif scenario == Scenario.MALFORMED:
        response["output"] = "invalid output items"
        events = [("response.output_text.delta", {"type": "response.output_text.delta", "delta": 42})]
    elif scenario == Scenario.UNSUPPORTED:
        events.insert(2, ("response.future_event", {"type": "response.future_event", "sequence_number": 2}))
        for index, (_, payload) in enumerate(events):
            payload["sequence_number"] = index
    return response, events
