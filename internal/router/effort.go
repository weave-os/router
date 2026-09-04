package router

import "strings"

const (
	effortLow    = "low"
	effortMedium = "medium"
	effortHigh   = "high"
	effortMax    = "max"
	effortXhigh  = "xhigh"
)

// CanonicalizeEffort maps user-facing aliases to canonical routing effort levels.
func CanonicalizeEffort(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "fast", "low", "minimal", "min":
		return effortLow
	case "medium", "med":
		return effortMedium
	case "high":
		return effortHigh
	case "max":
		return effortMax
	case "ultra", "xhigh":
		return effortXhigh
	default:
		return level
	}
}

// HigherEffort returns whichever of a and b sits higher on the reasoning
// ladder, canonicalized. An unrecognized level loses to a recognized one.
func HigherEffort(a, b string) string {
	rankA, okA := effortRank(a)
	rankB, okB := effortRank(b)
	switch {
	case !okA:
		return CanonicalizeEffort(b)
	case !okB:
		return CanonicalizeEffort(a)
	case rankB > rankA:
		return CanonicalizeEffort(b)
	default:
		return CanonicalizeEffort(a)
	}
}

func effortRank(level string) (int, bool) {
	switch CanonicalizeEffort(level) {
	case effortLow:
		return 0, true
	case effortMedium:
		return 1, true
	case effortHigh:
		return 2, true
	case effortMax:
		return 3, true
	case effortXhigh:
		return 4, true
	default:
		return 0, false
	}
}

// IsValidEffort reports whether level is a recognized routing effort level or alias.
func IsValidEffort(level string) bool {
	switch CanonicalizeEffort(level) {
	case effortLow, effortMedium, effortHigh, effortMax, effortXhigh:
		return true
	default:
		return false
	}
}
