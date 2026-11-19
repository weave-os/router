package policyclient

import (
	"encoding/json"
	"fmt"
	"net/http"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type routeResponse struct {
	SchemaVersion        string                 `json:"schema_version"`
	RouteID              string                 `json:"route_id"`
	SelectedArmID        string                 `json:"selected_arm_id"`
	SelectedRosterID     string                 `json:"selected_roster_id"`
	SelectedProvider     string                 `json:"selected_provider"`
	Model                string                 `json:"model"`
	Score                float64                `json:"score"`
	ChosenScore          *float64               `json:"chosen_score"`
	CandidateScores      map[string]float32     `json:"candidate_scores"`
	ScoreKind            string                 `json:"score_kind"`
	ScoreLabel           string                 `json:"score_label"`
	Reason               string                 `json:"reason"`
	PolicyState          string                 `json:"policy_state"`
	StateLabel           string                 `json:"state_label"`
	PolicyGroup          string                 `json:"policy_group"`
	Cluster              string                 `json:"cluster"`
	PolicyLabel          string                 `json:"policy_label"`
	ComplexityLabel      string                 `json:"complexity_label"`
	PolicyRouteKey       string                 `json:"policy_route_key"`
	RoutingBucket        string                 `json:"routing_bucket"`
	Confidence           *float64               `json:"confidence"`
	ClassifierConfidence *float64               `json:"classifier_confidence"`
	Margin               *float64               `json:"margin"`
	ClassifierMargin     *float64               `json:"classifier_margin"`
	Propensity           float64                `json:"propensity"`
	DisplayMarker        string                 `json:"display_marker"`
	PolicyArtifactID     string                 `json:"policy_artifact_id"`
	PolicyModelID        string                 `json:"policy_model_id"`
	PolicyArtifactSHA256 string                 `json:"policy_artifact_sha256"`
	PolicySHA256         string                 `json:"policy_sha256"`
	RosterVersion        string                 `json:"roster_version"`
	DebugRef             string                 `json:"debug_ref"`
	Debug                map[string]interface{} `json:"debug"`
	RankedFallback       []policy.PreviewGroup  `json:"ranked_fallback"`
	ArmScores            map[string]float32     `json:"arm_scores"`
	PredictedLabel       string                 `json:"predicted_label"`
	ClassProbabilities   map[string]float64     `json:"class_probabilities"`
	Timings              *routeTimings          `json:"timings"`
	Error                string                 `json:"error"`
}

func WithExpectedPolicySHA256(digest string) Option {
	return func(c *Client) { c.expectedPolicySHA256 = digest }
}

func checkPolicyResponsePin(expected, digest, schema, routeID, expectedRouteID string) error {
	if expected != "" && (schema != policy.SchemaVersionV1 || routeID != expectedRouteID || digest != expected) {
		return fmt.Errorf("policy response does not match the pinned artifact and route: %w", router.ErrPolicyPinUnavailable)
	}
	return nil
}

func decodeRouteResponse(query policy.Query, status int, payload []byte, expectedPolicySHA256 string) (policy.Result, error) {
	var parsed routeResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return policy.Result{}, fmt.Errorf("decode policy route response (status %d): %w", status, err)
	}
	if status != http.StatusOK {
		if err := unknownArtifactErr(query, status); err != nil {
			return policy.Result{}, err
		}
		if parsed.Error != "" {
			return policy.Result{}, fmt.Errorf("policy sidecar status %d: %s", status, parsed.Error)
		}
		return policy.Result{}, fmt.Errorf("policy sidecar status %d", status)
	}
	policySHA256 := firstNonEmpty(parsed.PolicyArtifactSHA256, parsed.PolicySHA256)
	if err := checkPolicyResponsePin(expectedPolicySHA256, policySHA256, parsed.SchemaVersion, parsed.RouteID, query.RouteID); err != nil {
		return policy.Result{}, err
	}
	if parsed.SchemaVersion == policy.SchemaVersionV4 {
		classifier, decodeErr := decodeClassifierResponseV4(payload)
		if decodeErr != nil {
			return policy.Result{}, fmt.Errorf("decode policy route response (status %d): %w", status, decodeErr)
		}
		return policy.Result{
			SchemaVersion:         classifier.SchemaVersion,
			RouteID:               classifier.RouteID,
			Score:                 valueOrZero(classifier.ClassifierConfidence),
			Confidence:            classifier.ClassifierConfidence,
			Margin:                classifier.ClassifierMargin,
			PolicyArtifactID:      classifier.ClassifierArtifactID,
			PolicyArtifactSHA256:  classifier.ClassifierSHA256,
			DebugRef:              classifier.DebugRef,
			Debug:                 classifier.Debug,
			PredictedLabel:        classifier.PredictedLabel,
			ClassOrder:            classifier.ClassOrder,
			ClassProbabilities:    classifier.ClassProbabilities,
			HMMStateID:            classifier.HMMStateID,
			HMMStatePath:          classifier.HMMStatePath,
			HMMStateProbabilities: classifier.HMMStateProbabilities,
			Timings:               decomposeTimings(classifier.Timings),
			ServingStats:          extractServingStats(classifier.Timings),
		}, nil
	}
	selectedModel := firstNonEmpty(parsed.SelectedRosterID, parsed.Model)
	switch parsed.SchemaVersion {
	case policy.SchemaVersionV3:
		if parsed.SelectedArmID != "" || selectedModel != "" || parsed.SelectedProvider != "" {
			return policy.Result{}, fmt.Errorf("policy sidecar returned a selected arm or provider on schema %s", policy.SchemaVersionV3)
		}
		if len(parsed.RankedFallback) == 0 {
			return policy.Result{}, fmt.Errorf("policy sidecar returned no ranked fallback on schema %s", policy.SchemaVersionV3)
		}
	case "", policy.SchemaVersionV1, policy.SchemaVersionV2:
		if parsed.SelectedArmID == "" && selectedModel == "" {
			return policy.Result{}, fmt.Errorf("policy sidecar returned empty arm and model")
		}
	default:
		return policy.Result{}, fmt.Errorf("unsupported policy route schema %q", parsed.SchemaVersion)
	}
	score := parsed.Score
	if parsed.ChosenScore != nil {
		score = *parsed.ChosenScore
	}
	return policy.Result{
		SchemaVersion:        parsed.SchemaVersion,
		RouteID:              parsed.RouteID,
		ArmID:                parsed.SelectedArmID,
		Model:                selectedModel,
		Provider:             parsed.SelectedProvider,
		Score:                score,
		CandidateScores:      parsed.CandidateScores,
		ScoreKind:            firstNonEmpty(parsed.ScoreKind, parsed.ScoreLabel),
		Reason:               parsed.Reason,
		PolicyState:          firstNonEmpty(parsed.PolicyState, parsed.StateLabel),
		PolicyGroup:          firstNonEmpty(parsed.PolicyGroup, parsed.Cluster),
		PolicyLabel:          firstNonEmpty(parsed.PolicyLabel, parsed.ComplexityLabel),
		PolicyRouteKey:       firstNonEmpty(parsed.PolicyRouteKey, parsed.RoutingBucket),
		Confidence:           firstFloat(parsed.Confidence, parsed.ClassifierConfidence),
		Margin:               firstFloat(parsed.Margin, parsed.ClassifierMargin),
		Propensity:           parsed.Propensity,
		DisplayMarker:        parsed.DisplayMarker,
		PolicyArtifactID:     firstNonEmpty(parsed.PolicyArtifactID, parsed.PolicyModelID),
		PolicyArtifactSHA256: policySHA256,
		RosterVersion:        parsed.RosterVersion,
		DebugRef:             parsed.DebugRef,
		Debug:                parsed.Debug,
		RankedFallback:       parsed.RankedFallback,
		ArmScores:            parsed.ArmScores,
		PredictedLabel:       parsed.PredictedLabel,
		ClassProbabilities:   parsed.ClassProbabilities,
		Timings:              decomposeTimings(parsed.Timings),
		ServingStats:         extractServingStats(parsed.Timings),
	}, nil
}
