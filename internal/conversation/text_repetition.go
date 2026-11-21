package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"weave-os/router/internal/translate"
)

const (
	TextRepetitionWindow    = 12
	textRepetitionThreshold = 3
	textRepetitionMinLen    = 40
)

func normalizeAssistantText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

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
