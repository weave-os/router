from __future__ import annotations

import shlex

import pytest

from weave_bench.agent.beta_turn import beta_acknowledged, is_fresh_exec, with_beta_turn

HARBOR_COMMAND = (
    "if [ -s ~/.nvm/nvm.sh ]; then . ~/.nvm/nvm.sh; fi; "
    "codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check "
    "--model gpt-5.6-sol --json --enable unified_exec -- "
    "'Please resume -- the task' 2>&1 </dev/null | tee /logs/agent/codex.txt"
)


def test_fresh_exec_is_detected_from_flags_not_instruction() -> None:
    assert is_fresh_exec(HARBOR_COMMAND)
    assert not is_fresh_exec(HARBOR_COMMAND.replace("codex exec ", "codex exec resume --last "))
    assert not is_fresh_exec("mkdir -p /logs/agent")


def test_beta_turn_precedes_a_native_resume_with_the_same_flags() -> None:
    beta_turn, task_turn = with_beta_turn(HARBOR_COMMAND, "/logs/agent/codex-beta.txt").split("\n")
    assert beta_turn.startswith("if [ -s ~/.nvm/nvm.sh ]; then . ~/.nvm/nvm.sh; fi; codex exec --dangerously")
    assert beta_turn.endswith(f"-- {shlex.quote('/beta')} 2>&1 </dev/null | tee /logs/agent/codex-beta.txt")
    assert "--model gpt-5.6-sol --json --enable unified_exec" in beta_turn
    assert task_turn.startswith(
        "if [ -s ~/.nvm/nvm.sh ]; then . ~/.nvm/nvm.sh; fi; codex exec resume --last --dangerously"
    )
    assert task_turn.endswith("-- 'Please resume -- the task' 2>&1 </dev/null | tee /logs/agent/codex.txt")
    assert not is_fresh_exec(task_turn)


def test_unexpected_command_shape_is_rejected() -> None:
    with pytest.raises(ValueError):
        with_beta_turn("codex exec --json", "/x")


def test_ack_detection() -> None:
    assert beta_acknowledged('{"type":"item.completed","text":"✦ **Weave Router** → Beta enabled. Type /beta again"}')
    assert not beta_acknowledged('{"type":"error","message":"invalid_key"}')
