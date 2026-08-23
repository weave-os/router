package proxy

import (
	"strings"

	"workweave/router/internal/router"
)

// effortHysteresisThreshold is the minimum WMI-score gap required for an
// effort-only change on the same base model within the same cluster.
const effortHysteresisThreshold = 1.0

// incumbentEffortForHold returns the effort level the previous turn served,
// adapting the bare "model" in LastServedModel (pre-effort pins) to "" so
// the gap test below doesn't hold an old bare pin against a new effort arm.
func incumbentEffortForHold(prevServedModel string) string {
	if idx := strings.LastIndex(prevServedModel, ":"); idx > 0 {
		return prevServedModel[idx+1:]
	}
	return ""
}

// challengerEffortForHold extracts the effort the fresh decision would serve.
func challengerEffortForHold(decisionEffort string, selectedArmID string) string {
	if decisionEffort != "" {
		return decisionEffort
	}
	if idx := strings.LastIndex(selectedArmID, ":"); idx > 0 {
		return selectedArmID[idx+1:]
	}
	return ""
}

// effortHysteresisHold decides whether to keep the incumbent effort.  The
// guard fires when: the fresh pick is the same base model served last turn,
// both turns carry an effort level, and the challenger's WMI gain is below
// the threshold.
//
// Scores are keyed by roster arm ID ("anthropic/claude-opus-5:xhigh"), which
// is what the sidecar reports.  The challenger's score comes from
// freshMetadata.ArmScores (the current-turn sidecar response).  The
// incumbent's score is found by searching the same map for a key matching
// the base model + previous effort.
//
// Returns the effort to serve: challenger if switch is allowed, incumbent
// if held.  Returns challenger unchanged when data is absent (pass-through).
func effortHysteresisHold(
	freshMetadata *router.RoutingMetadata,
	prevServedModel string,
	chosenEffort string,
) string {
	if freshMetadata == nil || freshMetadata.ArmScores == nil {
		return ""
	}
	incumbentEffort := incumbentEffortForHold(prevServedModel)
	if incumbentEffort == "" {
		return ""
	}
	challengerEffort := challengerEffortForHold(chosenEffort, freshMetadata.SelectedArmID)
	if challengerEffort == "" || incumbentEffort == challengerEffort {
		return challengerEffort
	}
	if !sameBaseArmID(freshMetadata.SelectedArmID, prevServedModel) {
		return challengerEffort
	}
	challengerScore := freshMetadata.ArmScores[freshMetadata.SelectedArmID]
	if challengerScore == 0 {
		return challengerEffort
	}
	incumbentScore := findArmScore(freshMetadata.ArmScores, prevServedModel, incumbentEffort)
	if incumbentScore == 0 {
		return challengerEffort
	}
	if challengerScore-incumbentScore >= effortHysteresisThreshold {
		return challengerEffort
	}
	return incumbentEffort
}

// sameBaseArmID reports whether the two identities share a base model by
// matching prefixes up to the last ":" in each.
func sameBaseArmID(rosterArmID, catalogServedIdentity string) bool {
	baseA, _, _ := splitArmID(rosterArmID)
	baseB, _, _ := splitArmID(catalogServedIdentity)
	return baseA != "" && baseB != "" && baseA == baseB
}

func splitArmID(id string) (base, provider, model string) {
	if idx := len(id) - 1; idx > 0 {
		for ; idx > 0; idx-- {
			if id[idx] == ':' {
				pref := strings.LastIndex(id[:idx], "/")
				if pref > 0 {
					return id[:idx], id[:pref], id[pref+1 : idx]
				}
				return id[:idx], "", id[:idx]
			}
		}
	}
	return id, "", ""
}

// findArmScore looks up the score for a model:effort combination in the
// sidecar's score map.  Tries exact match first, then prefix match on the
// bare model (e.g. "claude-opus-5:low" matches "anthropic/claude-opus-5:low").
func findArmScore(scores map[string]float32, servedIdentity string, effort string) float32 {
	exactKey := servedIdentity
	if !strings.Contains(servedIdentity, "/") {
		exactKey = strings.TrimPrefix(servedIdentity, "")
	}
	if s, ok := scores[exactKey]; ok && s != 0 {
		return s
	}
	suffix := "/" + extractModel(servedIdentity) + ":" + effort
	for key, s := range scores {
		if strings.HasSuffix(key, suffix) && s != 0 {
			return s
		}
	}
	return 0
}

func extractModel(servedIdentity string) string {
	if idx := strings.Index(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	if idx := strings.LastIndex(servedIdentity, "/"); idx > 0 {
		return servedIdentity[idx+1:]
	}
	return servedIdentity
}
