# llmescalation

Asynchronous LLM judging of agent trajectories: a classifier reads a rendered interval of the session and may raise its routing floor. Active and shadow experiments keep separate state (`Mode`).

- `prompt.md` and `schema.json` are unmodified Apache-2.0 Switchyard assets and `transcript.go` adapts Switchyard's Unicode-aware condenser (`escalation.rs` at `SwitchyardRevision`); attribution and license are in `NOTICE` and `LICENSE.switchyard`.
- `Store` implementations own short transactions: no lock spans a judge invocation.
- Persisted jobs are bounded audit metadata. Neither the transcript nor raw provider content is stored, and `FailureCode` is never an upstream response body.
- `RenderQwenInterval` waits for five earlier turns because training excluded windows 0..4, and a checkpoint judges only after `MinTurnsAfterUserPrompt` agent-only turns, so the agent is not punished for input it has not acted on. A history renumbered by compaction resumes judging once five post-compaction turns precede the interval.
