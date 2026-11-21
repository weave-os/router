package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
)

func candidateScoreFor(ports continuity.StrategyPorts, dec router.Decision, servedIdentity string) *float64 {
	return candidateScoreForWithProvider(ports, dec, servedIdentity, dec.Provider)
}

func candidateScoreForWithProvider(ports continuity.StrategyPorts, dec router.Decision, servedIdentity, provider string) *float64 {
	return candidateScoreForWithArm(ports, dec, servedIdentity, provider, "")
}

func candidateScoreForWithArm(ports continuity.StrategyPorts, dec router.Decision, servedIdentity, provider, rosterArmID string) *float64 {
	return ports.Facts.CandidateScore(dec, servedIdentity, provider, rosterArmID)
}
