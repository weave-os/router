# internal/router/percallband

Per-API-call band head. Given a session's trajectory of derived actions and
timings (`State`) and, on `MainLoop` turns, the prompt embedding, `Head.PredictBand`
predicts whether the agent's next call is a LARGE-band (stronger pinned model) or
SMALL-band (cheaper paired model) action. Pure and I/O-free; the boosters and
thresholds are embedded from `artifacts/`.

The head runs shadow-only until its Layer-2 extrinsic A/B clears
(`router-internal/docs/plans/PLAN_percall_band_swap_layer2.md`).

## Python parity

The action taxonomy, tool-to-action map, and feature layout mirror
`ml_dev/router_action_classifier/percall/{actions,features}.py`, and the artifacts
are exported by `percall/export.py`. Train-time labels and serve-time features must
not drift:

- `ActionTypes` order and `FeatureNames` order define the one-hot and histogram
  columns; `features_test.go` and `head_test.go` compare them, the feature builder
  and the inference output against frozen fixtures in `testdata/`.
- `New` fails when the embedded metadata's feature names differ from
  `FeatureNames`.
- Features come only from past calls plus the current request, never the current
  response, so they reproduce at routing time.
- `distinct_actions` reproduces a quirk of the training code: it is 0 on a
  session's first call and `len(ActionTypes)` on every later call.

## Session state

`State.Advance` runs after each served call with the response-derived action
(`DeriveAction`); `State.Features` reads it at the next call's decision time.
`Snapshot` / `StateFromSnapshot` persist the state per session pin as bounded
aggregates, copying maps and slices so neither side aliases the other.
