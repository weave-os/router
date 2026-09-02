// Package intent derives small, explainable routing signals from request
// structure and a bounded prompt excerpt. It never calls a model or performs
// I/O, so an intent signal can only influence preference, not availability.
package intent

import (
	"sort"
	"strings"
	"unicode"
)

const (
	// TagCoding marks software-development requests.
	TagCoding = "coding"
	// TagAgentic marks requests that orchestrate tools or multi-step work.
	TagAgentic = "agentic"
	// TagVision marks requests containing image input.
	TagVision = "vision"
	// TagLongContext marks requests whose input is large enough to need an
	// extended-context candidate.
	TagLongContext = "long_context"
	// TagDeepReasoning marks requests with explicit analysis or design intent.
	TagDeepReasoning = "deep_reasoning"
	// TagSummarization marks requests asking to condense or summarize content.
	TagSummarization = "summarization"
)

// Signals are the privacy-bounded facts used by Detect. PromptText should be
// the ingress-selected excerpt, never an unbounded raw request body.
type Signals struct {
	PromptText           string
	HasTools             bool
	HasImages            bool
	EstimatedInputTokens int
}

const longContextTokenThreshold = 32_000

var keywordTags = map[string]string{
	"api":          TagCoding,
	"build":        TagCoding,
	"bug":          TagCoding,
	"code":         TagCoding,
	"compile":      TagCoding,
	"debug":        TagCoding,
	"function":     TagCoding,
	"implement":    TagCoding,
	"repository":   TagCoding,
	"test":         TagCoding,
	"tool":         TagAgentic,
	"workflow":     TagAgentic,
	"architecture": TagDeepReasoning,
	"analyze":      TagDeepReasoning,
	"compare":      TagDeepReasoning,
	"design":       TagDeepReasoning,
	"explain":      TagDeepReasoning,
	"prove":        TagDeepReasoning,
	"reason":       TagDeepReasoning,
	"summarize":    TagSummarization,
	"summary":      TagSummarization,
	"condense":     TagSummarization,
}

// Detect returns deterministic tags in stable order. Structural signals win
// over text, and a keyword must be a complete token so “testing” does not
// accidentally become a coding request because it contains “test”.
func Detect(signals Signals) []string {
	tags := make(map[string]struct{}, 4)
	if signals.HasImages {
		tags[TagVision] = struct{}{}
	}
	if signals.HasTools {
		tags[TagAgentic] = struct{}{}
	}
	if signals.EstimatedInputTokens >= longContextTokenThreshold {
		tags[TagLongContext] = struct{}{}
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(signals.PromptText), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if tag, ok := keywordTags[word]; ok {
			tags[tag] = struct{}{}
		}
	}
	out := make([]string, 0, len(tags))
	for tag := range tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
