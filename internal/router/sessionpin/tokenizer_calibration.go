package sessionpin

import (
	"context"
	"maps"
	"time"

	"weave-os/router/internal/router"
)

type TokenizerFamily string

const (
	TokenizerFamilyAnthropic TokenizerFamily = "anthropic"
	TokenizerFamilyOpenAI    TokenizerFamily = "openai"
)

type TokenizerPair string

func NewTokenizerPair(from, to TokenizerFamily) TokenizerPair {
	return TokenizerPair(string(from) + "->" + string(to))
}

type TokenizerObservation struct {
	Prompt     int       `json:"prompt"`
	Estimate   int       `json:"estimate,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type TokenizerRatioMeasurement struct {
	Ratio      float64   `json:"ratio"`
	MeasuredAt time.Time `json:"measured_at"`
}

type TokenizerCalibration struct {
	Observations map[TokenizerFamily]TokenizerObservation    `json:"observations,omitempty"`
	Ratios       map[TokenizerPair]TokenizerRatioMeasurement `json:"ratios,omitempty"`
}

const (
	TokenizerCalibrationMaxGap    = 2 * time.Minute
	TokenizerCalibrationMinPrompt = 30_000
)

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

func (c TokenizerCalibration) Observe(family TokenizerFamily, prompt, estimate int, at time.Time) TokenizerCalibration {
	if family == "" || prompt <= 0 {
		return c
	}
	next := c.clone()
	if next.Observations == nil {
		next.Observations = map[TokenizerFamily]TokenizerObservation{}
	}
	next.Observations[family] = TokenizerObservation{Prompt: prompt, Estimate: estimate, ObservedAt: at}
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

var windowFloorRatios = map[TokenizerPair]float64{
	NewTokenizerPair(TokenizerFamilyOpenAI, TokenizerFamilyAnthropic): 1.5,
	NewTokenizerPair(TokenizerFamilyAnthropic, TokenizerFamilyOpenAI): 1 / 1.8,
}

func (c TokenizerCalibration) ProjectedPrompt(family TokenizerFamily, estimate int) (int, bool) {
	if family == "" || estimate <= 0 {
		return 0, false
	}
	var from TokenizerFamily
	var latest TokenizerObservation
	for f, obs := range c.Observations {
		if obs.Estimate <= 0 || obs.Prompt <= 0 || (from != "" && !obs.ObservedAt.After(latest.ObservedAt)) {
			continue
		}
		from, latest = f, obs
	}
	if from == "" {
		return 0, false
	}
	ratio, ok := c.Ratio(from, family)
	if !ok {
		ratio, ok = windowFloorRatios[NewTokenizerPair(from, family)]
	}
	if !ok {
		return 0, false
	}
	return int(float64(estimate) * float64(latest.Prompt) / float64(latest.Estimate) * ratio), true
}

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

type TokenizerCalibrationStore interface {
	UpdateTokenizerCalibration(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy, cal TokenizerCalibration) error
}
