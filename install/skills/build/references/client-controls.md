# Client controls

## Claude Code

- Dispatch workers with the Agent tool using the installed agent types:
  `build-researcher`, `build-implementer`, `build-verifier`,
  `build-reviewer`. Each has its own tool restrictions and none sets a
  model, so the router chooses one per assignment.
- Never pass a `model` argument to the Agent tool.
- If an agent type is missing, report that the build agents are not installed
  and stop.
- Run independent researchers or implementers in one message so they run in
  parallel.

## Codex

- Dispatch each worker with `spawn_agent`, putting the role template in the
  task message. Do not fork your turns into workers and do not set a model;
  the router picks it from the assignment.
- Give every worker a descriptive `task_name` that states its role and
  scope, for example `research_find_cli_entrypoint`,
  `implement_add_json_flag`, `verify_tests_and_edge_cases` or
  `review_whole_change`. Codex encrypts the task message, so the task name
  is the only part of the assignment the router can read.
- Start every worker message with its role line, for example
  `Role: build-researcher.`, so its scope is unambiguous.
- Wait for all parallel workers before integrating their results.

## Both

- The coordinator pin comes from `scripts/pin-coordinator.sh`. Set
  `WEAVE_BUILD_COORDINATOR_MODEL` to pin a different coordinator model.
- Worker models are chosen per assignment by the Weave Router. Without the
  router, workers use the client's normal model selection.
