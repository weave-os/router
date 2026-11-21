package hmm

import (
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/routingpolicy"
)

func CandidateScore(dec router.Decision, servedIdentity, provider, rosterArmID string) *float64 {
	if servedIdentity == "" || dec.Metadata == nil {
		return nil
	}
	if score, ok := dec.Metadata.CandidateScores[candidates.BaseModelOf(servedIdentity)]; ok {
		value := float64(score)
		return &value
	}

	if rosterArmID != "" {
		if score, ok := dec.Metadata.ArmScores[rosterArmID]; ok {
			value := float64(score)
			return &value
		}
	}

	effort, model := routingpolicy.StripEffortSuffix(servedIdentity)
	if model == "" || len(dec.Metadata.ArmScores) == 0 {
		return nil
	}
	armSuffix := ""
	if effort != "" {
		armSuffix = ":" + effort
	}
	keys := make([]string, 0, 2)
	if provider != "" {
		keys = append(keys, provider+"/"+model+armSuffix)
	}
	keys = append(keys, model+armSuffix)
	for _, key := range keys {
		if score, ok := dec.Metadata.ArmScores[key]; ok {
			value := float64(score)
			return &value
		}
	}

	var fallback *float64
	for armID, score := range dec.Metadata.ArmScores {
		armEffort, armModel := routingpolicy.StripEffortSuffix(armID)
		if armEffort != effort || (armModel != model && !strings.HasSuffix(armModel, "/"+model)) {
			continue
		}
		if armProvider, ok := dec.Metadata.CandidateArmProviders[armID]; ok && provider != "" && armProvider == provider {
			value := float64(score)
			return &value
		}
		if fallback != nil {
			return nil
		}
		value := float64(score)
		fallback = &value
	}
	return fallback
}
