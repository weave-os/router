package handover

type Outcome struct {
	Invoked               bool
	LatencyMS             int64
	SummaryTokens         int
	FallbackToFullHistory bool
	SummaryUsage          Usage
}
