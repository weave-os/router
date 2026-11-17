package inference

import "time"

type AccountingOutcome string

const (
	AccountingOutcomeBilled       AccountingOutcome = "billed"
	AccountingOutcomeUnbilled     AccountingOutcome = "unbilled"
	AccountingOutcomeUsageUnknown AccountingOutcome = "usage_unknown"
	AccountingOutcomeFailed       AccountingOutcome = "failed"
)

type AttemptOutcome string

const (
	AttemptOutcomeServed  AttemptOutcome = "served"
	AttemptOutcomeFailed  AttemptOutcome = "failed"
	AttemptOutcomeSkipped AttemptOutcome = "skipped"
	AttemptOutcomeAborted AttemptOutcome = "aborted"
)

type Usage struct {
	Known               bool
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
}

type Provenance struct {
	Purpose          Purpose
	PolicyID         PolicyID
	RegistryRevision PolicyRevision
	PolicyRevision   PolicyRevision
}

type OperationSummary struct {
	Provenance
	PlanTarget        Target
	ServedTarget      Target
	FallbackReason    string
	AccountingOutcome AccountingOutcome
	Usage             Usage
}

type FundingSource string

const FundingSourceIncludedCodex FundingSource = "codex_subscription"

type CapacitySource string

const CapacitySourceLinkedCodex CapacitySource = "linked_codex"

type AttemptFunding struct {
	SubscriptionAccountID string
	Source                FundingSource
	CapacitySource        CapacitySource
	PoolID                string
	PolicySHA256          string
	ObservedAt            time.Time
}

type AttemptEvent struct {
	Funding AttemptFunding
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
	CostUSD            float64
	Diagnosis          AttemptDiagnosis
}

type AttemptDiagnosis struct {
	ErrorClass     string
	FailureOwner   string
	BodyTranslated bool
}
