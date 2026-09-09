package inference

import "time"

// AccountingOutcome states how an operation's usage was accounted. Unknown
// usage is explicit: it is never recorded as a zero-cost billed operation.
type AccountingOutcome string

const (
	AccountingOutcomeBilled       AccountingOutcome = "billed"
	AccountingOutcomeUnbilled     AccountingOutcome = "unbilled"
	AccountingOutcomeUsageUnknown AccountingOutcome = "usage_unknown"
	AccountingOutcomeFailed       AccountingOutcome = "failed"
)

// AttemptOutcome classifies one upstream attempt.
type AttemptOutcome string

const (
	AttemptOutcomeServed  AttemptOutcome = "served"
	AttemptOutcomeFailed  AttemptOutcome = "failed"
	AttemptOutcomeSkipped AttemptOutcome = "skipped"
	AttemptOutcomeAborted AttemptOutcome = "aborted"
)

// Usage is upstream-reported token usage. Known is false when the upstream
// reported nothing; the counts are then unmeasured rather than zero.
type Usage struct {
	Known               bool
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
}

// Provenance identifies the policy entry and revisions that produced a plan.
// The same values appear in the request summary, every attempt event,
// generated policy documentation, and runtime inspection so a revision can be
// compared across all of them.
type Provenance struct {
	Purpose          Purpose
	PolicyID         PolicyID
	RegistryRevision PolicyRevision
	PolicyRevision   PolicyRevision
}

// OperationSummary is the bounded provenance an operation contributes to the
// request summary row. FallbackReason is empty when the served target is the
// policy-selected target.
type OperationSummary struct {
	Provenance
	PlanTarget        Target
	ServedTarget      Target
	FallbackReason    string
	AccountingOutcome AccountingOutcome
	Usage             Usage
}

// AttemptEvent is one ordered upstream attempt of an operation. OperationID
// distinguishes operations within one request (a main turn and its handover
// summary) so attempt ordering never collides with request-summary
// uniqueness. FailureReason is a bounded machine reason, never upstream body
// content.
type AttemptEvent struct {
	Provenance
	RequestID          string
	OperationID        string
	AttemptIndex       int
	Target             Target
	Outcome            AttemptOutcome
	FailureReason      string
	UpstreamStatusCode int
	Latency            time.Duration
	Usage              Usage
	// CostUSD is meaningful only when Usage.Known is true.
	CostUSD float64
}
