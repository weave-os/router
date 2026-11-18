package router

type ClientBudgetEvidence string

const (
	ClientBudgetUnknown              ClientBudgetEvidence = ""
	ClientBudgetHarnessDefault       ClientBudgetEvidence = "harness_default"
	ClientBudgetAmbiguousLongContext ClientBudgetEvidence = "ambiguous_long_context"
)

type ClientBudget struct {
	Evidence                ClientBudgetEvidence
	Version                 string
	ModelVariant1M          bool
	InboundContext1M        bool
	DefaultWindow           int
	DefaultCompactThreshold int
}
