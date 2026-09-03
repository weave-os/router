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

// IsValidEffort reports whether level is a recognized routing effort level or alias.
func IsValidEffort(level string) bool {
	switch CanonicalizeEffort(level) {
	case effortLow, effortMedium, effortHigh, effortMax, effortXhigh:
		return true
	default:
		return false
	}
}
