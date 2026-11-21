package routingpolicy

import (
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func HMMEffectiveInputUSDPer1M(model string, inputTokens int) (float64, bool) {
	price, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0, false
	}
	price = price.ForInputTokens(inputTokens)
	return price.InputUSDPer1M, true
}

func CacheablePrefixTokens(pin sessionpin.Pin, total int, prefixBroken bool) (int, bool) {
	if prefixBroken {
		return 0, true
	}
	cached := pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	prior := PriorPromptTokens(pin)
	if prior <= 0 {
		return 0, false
	}
	share := min(1.0, float64(cached)/float64(prior))
	return int(share * float64(total)), true
}

func PriorPromptTokens(pin sessionpin.Pin) int {
	if providers.FamilyFor(pin.Provider) == providers.FamilyAnthropic {
		return pin.LastInputTokens + pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	}
	return pin.LastInputTokens
}

func CacheWarm(pin sessionpin.Pin, now time.Time) bool {
	if pin.LastTurnEndedAt.IsZero() {
		return false
	}
	return now.Sub(pin.LastTurnEndedAt) < providers.CacheTTLFor(pin.Provider)
}

func PinCacheCold(pin sessionpin.Pin, prefixBroken bool, now time.Time) bool {
	return !CacheWarm(pin, now) || prefixBroken
}

func StripEffortSuffix(model string) (effort string, modelOut string) {
	const sep = ":"
	idx := strings.LastIndex(model, sep)
	if idx < 0 || idx == len(model)-1 {
		return "", model
	}
	tail := strings.TrimSpace(model[idx+1:])
	if !LooksLikeEffortAlias(tail) {
		return "", model
	}
	return router.CanonicalizeEffort(tail), model[:idx]
}

func LooksLikeEffortAlias(tail string) bool {
	switch strings.ToLower(strings.TrimSpace(tail)) {
	case "fast", "low", "medium", "med", "high", "max", "xhigh",
		"ultra", "minimal", "min":
		return true
	default:
		return false
	}
}
