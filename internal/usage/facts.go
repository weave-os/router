package usage

type Count struct {
	Value    int
	Reported bool
}

func Reported(value int) Count { return Count{Value: value, Reported: true} }

type InputBasis uint8

const (
	InputUnknown InputBasis = iota
	InputInclusive
	InputFresh
)

type OutputBasis uint8

const (
	OutputUnknown OutputBasis = iota
	OutputInclusive
	OutputExcludesReasoning
)

type Speed string

const SpeedFast Speed = "fast"

type InferenceGeo string

const InferenceGeoUS InferenceGeo = "us"

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

func (f Facts) PromptTokens() int {
	if f.InputBasis == InputFresh {
		return f.Input.Value + f.CacheWrite.Value + f.CacheRead.Value
	}
	return f.Input.Value
}

func (f Facts) FreshInputTokens() int {
	if f.InputBasis == InputFresh {
		return f.Input.Value
	}
	return max(0, f.Input.Value-f.CacheWrite.Value-f.CacheRead.Value)
}

func (f Facts) BilledOutputTokens() int {
	if f.OutputBasis == OutputExcludesReasoning {
		return f.Output.Value + f.Reasoning.Value
	}
	return f.Output.Value
}

func (f Facts) TotalTokens() int {
	if f.Total.Reported {
		return f.Total.Value
	}
	return f.PromptTokens() + f.BilledOutputTokens()
}

func (f Facts) CacheSplitReported() bool { return f.CacheWrite5m.Reported || f.CacheWrite1h.Reported }

type Sink interface{ RecordProviderUsage(Facts) }
