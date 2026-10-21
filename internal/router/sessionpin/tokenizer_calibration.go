package sessionpin

import (
	"context"
	"maps"
	"time"

	"weave-os/router/internal/router"
)

// TokenizerFamily names a token vocabulary. Bindings that share one bill the
// same history with the same count; across families the counts differ (the
// same Claude Code history bills roughly 1.5-1.8x more tokens on Claude than
// on GPT, varying with the mix of code, JSON and prose).
type TokenizerFamily string

const (
	// TokenizerFamilyAnthropic is the Claude tokenizer, used by every
	// Anthropic-format binding (first-party and gateways).
	TokenizerFamilyAnthropic TokenizerFamily = "anthropic"
	// TokenizerFamilyOpenAI is the GPT tokenizer, used by OpenAI and
	// OpenAI-compatible gateways serving OpenAI models.
	TokenizerFamilyOpenAI TokenizerFamily = "openai"
)

// TokenizerPair keys a measured conversion between two families.
type TokenizerPair string

// NewTokenizerPair keys the conversion from from-family tokens to to-family tokens.
func NewTokenizerPair(from, to TokenizerFamily) TokenizerPair {
	return TokenizerPair(string(from) + "->" + string(to))
}

// TokenizerObservation is the full prompt size one tokenizer family billed
// for this thread's history on its most recent served turn. Prompt is input
// plus cache reads plus cache writes, so it measures the whole history
// regardless of cache state.
type TokenizerObservation struct {
	Prompt     int       `json:"prompt"`
	ObservedAt time.Time `json:"observed_at"`
}

// TokenizerRatioMeasurement is a conversion measured from this thread's own
// traffic: tokens in the target family per token in the source family.
type TokenizerRatioMeasurement struct {
	Ratio      float64   `json:"ratio"`
	MeasuredAt time.Time `json:"measured_at"`
}

// TokenizerCalibration is a thread's evidence for converting token counts
// between tokenizer families. Observations hold the latest full-history count
// per family; Ratios hold conversions measured whenever two families served
// the thread close enough together to have seen nearly the same history.
// A measured ratio stays valid for the thread after the observations that
// produced it age out: production thread switches show a thread's ratio
// predicts its next switch to within 0.5% (median), against 4.3% for a
// fleet-wide average.
type TokenizerCalibration struct {
	Observations map[TokenizerFamily]TokenizerObservation    `json:"observations,omitempty"`
	Ratios       map[TokenizerPair]TokenizerRatioMeasurement `json:"ratios,omitempty"`
}

// Measurement bounds.
//
// Two observations are compared only when they are close in time, so the
// history each family billed is nearly the same: a Claude Code thread grows a
// few thousand tokens per turn, so the drift inside this window is small next
// to the counts. Small prompts are excluded because per-request overhead
// (system framing, tool schemas) differs by provider and dominates the ratio
// when the history itself is short.
const (
	TokenizerCalibrationMaxGap    = 2 * time.Minute
	TokenizerCalibrationMinPrompt = 30_000
)

// Ratio returns tokens in family to per token in family from, as measured on
// this thread. ok is false until this thread has been served by both families
// close together; callers then report the served family's own count.
func (c TokenizerCalibration) Ratio(from, to TokenizerFamily) (float64, bool) {
	if from == to {
		return 1, true
	}
	m, ok := c.Ratios[NewTokenizerPair(from, to)]
	if !ok || m.Ratio <= 0 {
		return 0, false
	}
	return m.Ratio, true
}

// Observe returns the calibration after family billed prompt tokens for the
// thread's history at at. When another family observed the thread within
// TokenizerCalibrationMaxGap, the conversion between the two is (re)measured
// in both directions. The receiver is not modified.
func (c TokenizerCalibration) Observe(family TokenizerFamily, prompt int, at time.Time) TokenizerCalibration {
	if family == "" || prompt <= 0 {
		return c
	}
	next := c.clone()
	if next.Observations == nil {
		next.Observations = map[TokenizerFamily]TokenizerObservation{}
	}
	next.Observations[family] = TokenizerObservation{Prompt: prompt, ObservedAt: at}
	if prompt < TokenizerCalibrationMinPrompt {
		return next
	}
	for other, obs := range c.Observations {
		if other == family || obs.Prompt < TokenizerCalibrationMinPrompt {
			continue
		}
		if gap := at.Sub(obs.ObservedAt); gap < 0 || gap > TokenizerCalibrationMaxGap {
			continue
		}
		if next.Ratios == nil {
			next.Ratios = map[TokenizerPair]TokenizerRatioMeasurement{}
		}
		next.Ratios[NewTokenizerPair(other, family)] = TokenizerRatioMeasurement{Ratio: float64(prompt) / float64(obs.Prompt), MeasuredAt: at}
		next.Ratios[NewTokenizerPair(family, other)] = TokenizerRatioMeasurement{Ratio: float64(obs.Prompt) / float64(prompt), MeasuredAt: at}
	}
	return next
}

// MergeTokenizerCalibrations combines calibrations read from several pin rows
// of the same thread, keeping the newest observation per family and the
// newest measurement per pair.
func MergeTokenizerCalibrations(cals ...TokenizerCalibration) TokenizerCalibration {
	var out TokenizerCalibration
	for _, c := range cals {
		for family, obs := range c.Observations {
			if cur, ok := out.Observations[family]; ok && !obs.ObservedAt.After(cur.ObservedAt) {
				continue
			}
			if out.Observations == nil {
				out.Observations = map[TokenizerFamily]TokenizerObservation{}
			}
			out.Observations[family] = obs
		}
		for pair, m := range c.Ratios {
			if cur, ok := out.Ratios[pair]; ok && !m.MeasuredAt.After(cur.MeasuredAt) {
				continue
			}
			if out.Ratios == nil {
				out.Ratios = map[TokenizerPair]TokenizerRatioMeasurement{}
			}
			out.Ratios[pair] = m
		}
	}
	return out
}

func (c TokenizerCalibration) clone() TokenizerCalibration {
	return TokenizerCalibration{
		Observations: maps.Clone(c.Observations),
		Ratios:       maps.Clone(c.Ratios),
	}
}

// TokenizerCalibrationStore persists a pin row's TokenizerCalibration. It is
// optional: a Store that does not implement it simply never calibrates, and
// cross-tokenizer usage is then reported in the served model's own tokens.
// A missing pin or a strategy mismatch is a no-op, matching UpdateUsage.
type TokenizerCalibrationStore interface {
	UpdateTokenizerCalibration(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy, cal TokenizerCalibration) error
}
