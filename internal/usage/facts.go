// Package usage owns provider token facts shared by translation, telemetry,
// and accounting. It has no transport, pricing, or lifecycle dependencies.
package usage

// Count distinguishes a reported zero from an absent provider field.
type Count struct {
	Value    int
	Reported bool
}

// Reported records a provider count, including zero.
func Reported(value int) Count { return Count{Value: value, Reported: true} }

// InputBasis describes whether reported input already includes cache tokens.
type InputBasis uint8

const (
	// InputUnknown denotes usage without a reported input convention.
	InputUnknown InputBasis = iota
	// InputInclusive includes cache reads and writes in Input.
	InputInclusive
	// InputFresh reports uncached input separately from cache reads and writes.
	InputFresh
)

// OutputBasis describes whether reported output already includes reasoning.
type OutputBasis uint8

const (
	// OutputUnknown denotes usage without a reported output convention.
	OutputUnknown OutputBasis = iota
	// OutputInclusive includes reasoning in Output.
	OutputInclusive
	// OutputExcludesReasoning reports visible output separately from reasoning.
	OutputExcludesReasoning
)

// Speed is a provider-reported serving speed.
type Speed string

// SpeedFast selects the provider's fast serving tier.
const SpeedFast Speed = "fast"

// InferenceGeo is a provider-reported inference geography.
type InferenceGeo string

// InferenceGeoUS identifies US-only inference.
const InferenceGeoUS InferenceGeo = "us"

// Facts retains reported token categories and their inclusion conventions.
// TTL splits are shares of cache writes; OutputBasis states whether reasoning
// is already included in Output.
type Facts struct {
	Present                             bool
	Input, Output, Total                Count
	Reasoning, CacheWrite, CacheRead    Count
	CacheWrite5m, CacheWrite1h          Count
	InputBasis                          InputBasis
	OutputBasis                         OutputBasis
	Speed                               Speed
	InferenceGeo                        InferenceGeo
	SpeedReported, InferenceGeoReported bool
}

// Merge applies cumulative provider snapshots; an omitted field cannot erase
// an earlier observation and repeated snapshots never add tokens.
func (f *Facts) Merge(update Facts) {
	if !update.Present {
		return
	}
	f.Present = true
	for _, pair := range []struct {
		dst *Count
		src Count
	}{
		{&f.Input, update.Input}, {&f.Output, update.Output}, {&f.Total, update.Total},
		{&f.Reasoning, update.Reasoning}, {&f.CacheWrite, update.CacheWrite}, {&f.CacheRead, update.CacheRead},
		{&f.CacheWrite5m, update.CacheWrite5m}, {&f.CacheWrite1h, update.CacheWrite1h},
	} {
		if pair.src.Reported {
			*pair.dst = pair.src
		}
	}
	if update.InputBasis != InputUnknown {
		f.InputBasis = update.InputBasis
	}
	if update.OutputBasis != OutputUnknown {
		f.OutputBasis = update.OutputBasis
	}
	if update.SpeedReported {
		f.Speed, f.SpeedReported = update.Speed, true
	}
	if update.InferenceGeoReported {
		f.InferenceGeo, f.InferenceGeoReported = update.InferenceGeo, true
	}
}

// PromptTokens returns cache-inclusive prompt length.
func (f Facts) PromptTokens() int {
	if f.InputBasis == InputFresh {
		return f.Input.Value + f.CacheWrite.Value + f.CacheRead.Value
	}
	return f.Input.Value
}

// FreshInputTokens returns uncached input without counting cache categories twice.
func (f Facts) FreshInputTokens() int {
	if f.InputBasis == InputFresh {
		return f.Input.Value
	}
	return max(0, f.Input.Value-f.CacheWrite.Value-f.CacheRead.Value)
}

// BilledOutputTokens includes separately reported reasoning exactly once.
func (f Facts) BilledOutputTokens() int {
	if f.OutputBasis == OutputExcludesReasoning {
		return f.Output.Value + f.Reasoning.Value
	}
	return f.Output.Value
}

// TotalTokens preserves a reported total; otherwise it derives a client total.
func (f Facts) TotalTokens() int {
	if f.Total.Reported {
		return f.Total.Value
	}
	return f.PromptTokens() + f.BilledOutputTokens()
}

// CacheSplitReported distinguishes a measured zero TTL share from no breakdown.
func (f Facts) CacheSplitReported() bool { return f.CacheWrite5m.Reported || f.CacheWrite1h.Reported }

// Sink receives facts parsed at the provider boundary.
type Sink interface{ RecordProviderUsage(Facts) }
