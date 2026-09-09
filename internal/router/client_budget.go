package router

// ClientBudgetEvidence describes what an inbound request establishes about its harness budget.
type ClientBudgetEvidence string

const (
	ClientBudgetUnknown              ClientBudgetEvidence = ""
	ClientBudgetHarnessDefault       ClientBudgetEvidence = "harness_default"
	ClientBudgetAmbiguousLongContext ClientBudgetEvidence = "ambiguous_long_context"
)

// ClientBudget is recomputed at ingress, never inherited from a session pin.
// Defaults are not effective limits: local overrides and account caps are private to the harness.
type ClientBudget struct {
	Evidence                ClientBudgetEvidence
	Version                 string
	ModelVariant1M          bool
	InboundContext1M        bool
	DefaultWindow           int
	DefaultCompactThreshold int
}
