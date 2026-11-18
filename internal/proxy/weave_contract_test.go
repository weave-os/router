package proxy_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/translate"
)

const (
	weaveContractGoldenPath   = "testdata/contracts/weave_contract.json"
	weaveForcedReasonPrefix   = "user_forced"
	weaveUtilityReasonSuffix  = "_hard_pin"
	telemetryInsertQueryPath  = "../../db/queries/model_router_request_telemetry.sql"
	telemetryInsertStatement  = "INSERT INTO router.model_router_request_telemetry ("
	routingSpanAttributeRoots = "../../internal"
)

var (
	weaveIngestedRoutingAttributes = []string{
		"routing.caller_passthrough", "routing.caller_routing_source", "routing.cluster_ids", "routing.cross_format",
		"routing.embed_cache_evictions", "routing.embed_cache_hits", "routing.embed_cache_misses", "routing.embed_input",
		"routing.estimated_input_tokens", "routing.llm_escalation_applied", "routing.prior_served_model", "routing.rollout_id",
		"routing.session_pin_tier", "routing.sidecar_inflight", "routing.sidecar_overruns_live", "routing.sticky_hit",
		"routing.task_arm_experiment_arm", "routing.task_arm_experiment_ceiling_binds", "routing.task_arm_experiment_control_cluster",
		"routing.task_arm_experiment_control_model", "routing.task_arm_experiment_propensity", "routing.task_arm_experiment_step",
		"routing.task_arm_experiment_task_key", "routing.task_arm_experiment_treatment_applied",
	}
	routingSpanAttributePattern = regexp.MustCompile(`^routing\.[a-z0-9_]+$`)
)

type weaveContract struct {
	TelemetryColumns         []string            `json:"model_router_request_telemetry_columns"`
	TelemetryVocabularies    map[string][]string `json:"model_router_request_telemetry_vocabularies"`
	DecisionClassification   map[string]string   `json:"decision_reason_classification"`
	AnalyticsSchemaVersion   string              `json:"analytics_schema_version"`
	AnalyticsFields          []analytics.Field   `json:"analytics_fields"`
	RoutingSpanAttributes    []string            `json:"routing_span_attributes"`
	WeaveIngestedAttributes  []string            `json:"weave_ingested_routing_span_attributes"`
	WeaveIngestedNotEmitted  []string            `json:"weave_ingested_routing_span_attributes_not_emitted"`
	ForcedClassificationKeys map[string]string   `json:"forced_classification_markers"`
}

func TestWeaveContractGolden(t *testing.T) {
	require.Equal(t, weaveForcedReasonPrefix, translate.ReasonUserForceModel, "Weave classifies a turn as forced by this decision_reason prefix")

	vocabularies, classification := goldenTelemetryVocabularies(t)
	attributes := routingSpanAttributes(t)
	var notEmitted []string
	for _, name := range weaveIngestedRoutingAttributes {
		if !slices.Contains(attributes, name) {
			notEmitted = append(notEmitted, name)
		}
	}
	contract := weaveContract{
		TelemetryColumns:        telemetryInsertColumns(t),
		TelemetryVocabularies:   vocabularies,
		DecisionClassification:  classification,
		AnalyticsSchemaVersion:  analytics.SchemaVersion,
		AnalyticsFields:         analytics.Schema(),
		RoutingSpanAttributes:   attributes,
		WeaveIngestedAttributes: weaveIngestedRoutingAttributes,
		WeaveIngestedNotEmitted: notEmitted,
		ForcedClassificationKeys: map[string]string{
			"forced_reason_prefix":  weaveForcedReasonPrefix,
			"utility_reason_suffix": weaveUtilityReasonSuffix,
		},
	}
	got, err := json.MarshalIndent(contract, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(weaveContractGoldenPath), 0o750))
		require.NoError(t, os.WriteFile(weaveContractGoldenPath, got, 0o600))
		return
	}
	want, err := os.ReadFile(weaveContractGoldenPath)
	require.NoError(t, err, "missing contract golden; run go test ./internal/proxy -run TestWeaveContractGolden -update")
	require.Equal(t, string(want), string(got), "a Weave-facing router contract changed; land the paired weave-os/weave change, then update with -update")
}

func telemetryInsertColumns(t *testing.T) []string {
	t.Helper()
	query, err := os.ReadFile(telemetryInsertQueryPath)
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(query), telemetryInsertStatement)
	require.True(t, found, "InsertRequestTelemetry statement not found")
	list, _, found := strings.Cut(rest, ")")
	require.True(t, found)
	var columns []string
	for _, column := range strings.Split(list, ",") {
		columns = append(columns, strings.TrimSpace(column))
	}
	return columns
}

func goldenTelemetryVocabularies(t *testing.T) (map[string][]string, map[string]string) {
	t.Helper()
	fields := map[string]string{
		"Strategy": "strategy", "DecisionReason": "decision_reason", "PlannerOutcome": "planner_outcome",
		"PinTier": "pin_tier", "StickyHit": "sticky_hit",
	}
	valuesByColumn := map[string]map[string]struct{}{}
	classification := map[string]string{}
	paths, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	require.NoError(t, err)
	for _, path := range paths {
		payload, err := os.ReadFile(path)
		require.NoError(t, err)
		var fixture struct {
			Requests []struct {
				Telemetry []map[string]any `json:"telemetry"`
			} `json:"requests"`
		}
		require.NoError(t, json.Unmarshal(payload, &fixture), path)
		for _, request := range fixture.Requests {
			for _, row := range request.Telemetry {
				for field, column := range fields {
					value, ok := row[field]
					if !ok {
						continue
					}
					text := vocabularyValue(value)
					if valuesByColumn[column] == nil {
						valuesByColumn[column] = map[string]struct{}{}
					}
					valuesByColumn[column][text] = struct{}{}
					if field == "DecisionReason" {
						classification[text] = forcedClassification(text)
					}
				}
			}
		}
	}
	vocabularies := make(map[string][]string, len(valuesByColumn))
	for column, set := range valuesByColumn {
		vocabularies[column] = sortedSet(set)
	}
	return vocabularies, classification
}

func vocabularyValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func forcedClassification(reason string) string {
	switch {
	case strings.HasPrefix(reason, weaveForcedReasonPrefix):
		return "forced"
	case strings.HasSuffix(reason, weaveUtilityReasonSuffix):
		return "utility"
	default:
		return "router_decision"
	}
}

func routingSpanAttributes(t *testing.T) []string {
	t.Helper()
	found := map[string]struct{}{}
	err := filepath.WalkDir(routingSpanAttributeRoots, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testdata") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				if value, err := strconv.Unquote(literal.Value); err == nil && routingSpanAttributePattern.MatchString(value) {
					found[value] = struct{}{}
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	return sortedSet(found)
}

func sortedSet(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}
