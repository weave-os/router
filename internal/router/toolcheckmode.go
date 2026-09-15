package router

import (
	"context"
	"strings"
)

// ToolCheckMode selects how much of the tool-call validation/repair pipeline
// (internal/translate/toolcheck) runs for a request. Values mirror
// toolcheck.Mode; the string form is the x-weave-toolcheck header spelling.
type ToolCheckMode string

const (
	// ToolCheckOff forwards model tool arguments as emitted (JSON syntax only).
	ToolCheckOff ToolCheckMode = "off"
	// ToolCheckStandard is the deployment default: normalize + validate +
	// lossless coercions.
	ToolCheckStandard ToolCheckMode = "standard"
	// ToolCheckSemantic adds meaning-preserving shape repairs on top of standard.
	ToolCheckSemantic ToolCheckMode = "semantic"
)

// ParseToolCheckMode accepts the header spelling of a mode.
func ParseToolCheckMode(raw string) (ToolCheckMode, bool) {
	switch m := ToolCheckMode(strings.ToLower(strings.TrimSpace(raw))); m {
	case ToolCheckOff, ToolCheckStandard, ToolCheckSemantic:
		return m, true
	}
	return "", false
}

type toolCheckModeContextKey struct{}

// WithToolCheckMode stashes a per-request toolcheck mode on ctx.
func WithToolCheckMode(ctx context.Context, m ToolCheckMode) context.Context {
	if m == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCheckModeContextKey{}, m)
}

// ToolCheckModeFromContext returns the per-request mode, ToolCheckStandard
// when none was set.
func ToolCheckModeFromContext(ctx context.Context) ToolCheckMode {
	m, ok := ctx.Value(toolCheckModeContextKey{}).(ToolCheckMode)
	if !ok || m == "" {
		return ToolCheckStandard
	}
	return m
}
