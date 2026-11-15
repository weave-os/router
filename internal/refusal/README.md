# internal/refusal

The single contract for how the router refuses a request for auth, billing,
entitlement and policy reasons. Each `Kind` maps to exactly one HTTP status,
machine-readable code and retry signal (`Contract`). Refusal sites name a
`Kind` and never pick a status themselves. `TestEveryKindHasAContract` and
`TestContractTable` pin the table.
