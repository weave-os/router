package rosterdata_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"

	"github.com/stretchr/testify/require"
)

const policyV1EveryFieldWire = `{"schema_version":"hmm_go_selection_policy_v1","comment":"Synthetic wire shape.","class_order":["low"],"ranking":{"metric":"wii_v1","alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"1111111111111111111111111111111111111111111111111111111111111111","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"2222222222222222222222222222222222222222222222222222222222222222","agentic_index_page":"synthetic-page","fallback_metric":"synthetic-fallback","snapshot_date":"2026-01-01","source":"synthetic-source","wii_v1":"synthetic description","wmi_filter":{"minimum_score_by_cluster":{"low":10},"min_arms_per_cluster":1,"max_arms_per_cluster":4}},"harness_vendor_priority":{"claude_code":{"vendors":["anthropic"],"clusters":["low"]}},"manual_pins":{"*":{"low":["openai/gpt-5.6-sol"]}},"preferences":{"preferred_model_bonus":0.5,"subscription_bonus":0.35},"subscription_ladders":{"anthropic":{"low":"anthropic/claude-sonnet-4.6"}},"provenance":{"source_revision":"synthetic-revision","evidence_uri":"synthetic://evidence","evidence_sha256":"3333333333333333333333333333333333333333333333333333333333333333"},"clusters":{"low":{"complexity_label":"low","arms":["openai/gpt-5.6-sol","anthropic/claude-haiku-4.5"],"arms_by_harness":{"claude_code":["anthropic/claude-haiku-4.5","openai/gpt-5.6-sol"]},"membership_by_harness":{"codex":["openai/gpt-5.6-sol"]},"cost_ref_usd":1,"latency_ref_ms":1000,"arm_scores":{"anthropic/claude-haiku-4.5":0.45,"openai/gpt-5.6-sol":0.76},"arm_indices":{"anthropic/claude-haiku-4.5":{"wii_v1":45,"wpi_v1":70},"openai/gpt-5.6-sol":{"wii_v1":76,"wpi_v1":38}},"manual_pins_by_harness":{"*":["openai/gpt-5.6-sol"]},"preferred_vendors_by_harness":{"claude_code":["anthropic"]}}}}`

const policyV1EveryFieldSHA256 = "b635046e29fe4b874e133c3b7779b65da5a19d7091b64e2d9da0bc8f698c447b"

func TestPolicyV1WireShapeIsPinned(t *testing.T) {
	digest := sha256.Sum256([]byte(policyV1EveryFieldWire))
	require.Equal(t, policyV1EveryFieldSHA256, hex.EncodeToString(digest[:]))

	roster, err := admitRoster([]byte(policyV1EveryFieldWire))
	require.NoError(t, err)
	require.Equal(t, policyV1EveryFieldSHA256, roster.SHA256)
	canonical, err := rosterdata.CanonicalBytes(roster)
	require.NoError(t, err)
	require.Equal(t, policyV1EveryFieldWire, string(canonical))
}

func TestPolicyV1WireRejectsUnknownAndModeFields(t *testing.T) {
	for name, rejection := range map[string]struct{ payload, reason string }{
		"unknown_top_level_field": {strings.Replace(policyV1EveryFieldWire, `{"schema_version"`, `{"unknown":true,"schema_version"`, 1), `unknown field "unknown"`},
		"mode_policies":           {strings.Replace(policyV1EveryFieldWire, `"preferences":`, `"mode_policies":{"plan":{"allowed_groups":["low"]}},"preferences":`, 1), `unknown field "mode_policies"`},
		"trailing_value":          {policyV1EveryFieldWire + ` {}`, "multiple JSON values"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := admitRoster([]byte(rejection.payload))
			require.ErrorContains(t, err, rejection.reason)
		})
	}
}

func admitRoster(payload []byte) (*rosterdata.Roster, error) {
	admitted, err := selectionpolicy.Admit(payload)
	if err != nil {
		return nil, err
	}
	return admitted.Roster(), nil
}
