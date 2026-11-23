package policy

import "slices"

func AllowsCompactionSummarizer(model string) bool {
	return slices.Contains(compactionSummarizerModels, model)
}

var compactionSummarizerModels = []string{
	PrecompactionDefaultModel,
	PrecompactionLargeWindowModel,
	"claude-sonnet-4-5",
	"claude-sonnet-4-6",
	"claude-sonnet-5",
	"claude-opus-4-0",
	"claude-opus-4-1",
	"claude-opus-4-5",
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-opus-5-5",
	"claude-fable-5",
	"claude-fable-5-1",
	"claude-fable-5-2",
}
