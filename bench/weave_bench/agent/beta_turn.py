"""Pure command rewriting for the ``/beta`` preflight (no Harbor import, unit-testable).

Harbor's Codex agent runs ``<prelude>codex exec <flags> -- <instruction> <pipe>``.
The router's ``/beta`` is a per-session toggle sent as the last user message, so a
fresh exec is split into a ``/beta`` turn that opens the thread and takes the
toggle, then ``codex exec resume --last <flags> -- <instruction>`` in that same
thread. ``resume`` accepts the full ``exec`` flag set, so Harbor's flags pass
through verbatim; only the flags before ``--`` are inspected, never the
instruction, so a task mentioning ``resume`` cannot change the shape.
"""

from __future__ import annotations

import shlex

BETA_COMMAND = "/beta"
BETA_TURN_OUTPUT_FILENAME = "codex-beta.txt"
BETA_ACK_MARKER = "Beta enabled"

_CODEX_EXEC = "codex exec "
_INSTRUCTION_SEPARATOR = " -- "
_RESUME_SUBCOMMAND = "resume "


def is_fresh_exec(command: str) -> bool:
    _, exec_marker, exec_tail = command.partition(_CODEX_EXEC)
    flags, _, _ = exec_tail.partition(_INSTRUCTION_SEPARATOR)
    return bool(exec_marker) and not flags.startswith(_RESUME_SUBCOMMAND)


def with_beta_turn(task_command: str, beta_output_path: str) -> str:
    prelude, _, exec_tail = task_command.partition(_CODEX_EXEC)
    flags, separator, instruction_and_pipe = exec_tail.partition(_INSTRUCTION_SEPARATOR)
    if not separator:
        raise ValueError(f"unexpected Harbor codex command shape: {task_command}")
    beta_turn = (
        f"{prelude}{_CODEX_EXEC}{flags}{_INSTRUCTION_SEPARATOR}"
        f"{shlex.quote(BETA_COMMAND)} 2>&1 </dev/null | tee {beta_output_path}"
    )
    task_turn = (
        f"{prelude}{_CODEX_EXEC}{_RESUME_SUBCOMMAND}--last {flags}{_INSTRUCTION_SEPARATOR}{instruction_and_pipe}"
    )
    return f"{beta_turn}\n{task_turn}"


def beta_acknowledged(beta_turn_output: str) -> bool:
    return BETA_ACK_MARKER in beta_turn_output
