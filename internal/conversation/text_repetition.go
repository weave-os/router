package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"weave-os/router/internal/translate"
)

// Enforcing assistant-text repetition detector.
//
// Existing detectors key on stagnation evidence (identical tool signatures,
// errored results, no tool activity). Fresh tool calls each turn advance the
// no-progress fingerprint while the assistant narrates the same intent
// verbatim — the repeated text is the only durable tell.
//
// Scans narration since the last real user turn; fires when a normalized text
// of ≥textRepetitionMinLen chars recurs ≥textRepetitionThreshold times within
// TextRepetitionWindow messages. Gated by ROUTER_TEXT_REPETITION_BREAK_ENABLED.
const (
	// TextRepetitionWindow bounds the scan to recent messages so an early
	// duplicate the agent recovered from doesn't count against it.
	TextRepetitionWindow = 12
	// textRepetitionThreshold: verbatim recurrences of one line that trip the
	// break. Three identical substantive turns is a loop, not a coincidence.
	textRepetitionThreshold = 3
	// textRepetitionMinLen: ignore short lines ("Done.", "OK") whose repetition
	// is benign; only count narration long enough to be a real intent restatement.
	textRepetitionMinLen = 40
)

// normalizeAssistantText lowercases and collapses whitespace so cosmetic
// drift (re-wrap, trailing newline) doesn't defeat the exact-match count.
func normalizeAssistantText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// DetectTextRepetition reports whether a normalized assistant text recurs
// textRepetitionThreshold+ times within TextRepetitionWindow messages. Returns
// count and an 8-byte hash of the offending text (safe for logs).
func DetectTextRepetition(env *translate.RequestEnvelope) (looped bool, count int, sampleHash string) {
	texts := env.TrailingAssistantTexts()
	if len(texts) < textRepetitionThreshold {
		return false, 0, ""
	}
	start := 0
	if len(texts) > TextRepetitionWindow {
		start = len(texts) - TextRepetitionWindow
	}
	window := texts[start:]

	counts := make(map[string]int, len(window))
	topKey := ""
	for _, t := range window {
		n := normalizeAssistantText(t)
		if len(n) < textRepetitionMinLen {
			continue
		}
		counts[n]++
		if counts[n] > count {
			count = counts[n]
			topKey = n
		}
	}
	if count < textRepetitionThreshold {
		return false, count, ""
	}
	h := sha256.Sum256([]byte(topKey))
	return true, count, hex.EncodeToString(h[:8])
}
