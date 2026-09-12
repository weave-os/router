package proxy

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"reflect"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"

	"weave-os/router/internal/observability"
)

func testObservationWorkers(t *testing.T) *observability.ObservationWorkers {
	t.Helper()
	workers := observability.NewObservationWorkers()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = workers.Shutdown(ctx)
	})
	return workers
}

type contextProbeKey struct{}

type snapshotTelemetryRepo struct {
	TelemetryRepository
	rows     chan InsertTelemetryParams
	contexts chan context.Context
}

func (r snapshotTelemetryRepo) InsertRequestTelemetry(ctx context.Context, p InsertTelemetryParams) error {
	r.rows <- p
	r.contexts <- ctx
	return nil
}

func TestTelemetrySnapshotPreservesMetadataAfterCancellation(t *testing.T) {
	workers := testObservationWorkers(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	workers.Database.Submit(observability.WorkAttempt, nil, time.Second, observability.FromContext(context.Background()), func(ctx context.Context, _ []byte) error {
		close(blocked)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-blocked
	repo := snapshotTelemetryRepo{rows: make(chan InsertTelemetryParams, 1), contexts: make(chan context.Context, 1)}
	svc := (&Service{telemetry: repo}).WithObservationWorkers(workers)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextProbeKey{}, "request-credential"))
	score := 0.7
	generation := int64(9007199254740993)
	timestamp := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	params := InsertTelemetryParams{RequestID: "req-snapshot", InstallationID: "install-snapshot", APIKeyID: "key-snapshot", Timestamp: timestamp, TrainingAllowed: true, DecisionModel: "served-model", DecisionProvider: "served-provider", ClusterIDs: []int32{3}, CandidateModels: []string{"candidate"}, CandidateScores: []byte(`{"candidate":0.7}`), ChosenScore: &score, SessionKey: []byte{1, 2}, SelectionHeadGeneration: &generation, Inference: &inference.OperationSummary{ServedTarget: inference.Target{Provider: "served-provider", CatalogID: "served-model"}}}
	svc.fireTelemetry(ctx, params)
	cancel()
	params.ClusterIDs[0] = 99
	params.CandidateModels[0] = "mutated"
	params.CandidateScores[0] = '!'
	params.SessionKey[0] = 9
	score = 0
	generation = 0
	params.Inference.ServedTarget.CatalogID = "mutated"
	close(release)
	var row InsertTelemetryParams
	select {
	case row = <-repo.rows:
	case <-time.After(time.Second):
		t.Fatal("telemetry snapshot not persisted")
	}
	require.Equal(t, "req-snapshot", row.RequestID)
	assert.Equal(t, "install-snapshot", row.InstallationID)
	assert.Equal(t, "key-snapshot", row.APIKeyID)
	assert.Equal(t, timestamp, row.Timestamp)
	assert.True(t, row.TrainingAllowed)
	assert.Equal(t, []int32{3}, row.ClusterIDs)
	assert.Equal(t, []string{"candidate"}, row.CandidateModels)
	assert.Equal(t, []byte(`{"candidate":0.7}`), row.CandidateScores)
	assert.Equal(t, []byte{1, 2}, row.SessionKey)
	require.NotNil(t, row.ChosenScore)
	assert.Equal(t, 0.7, *row.ChosenScore)
	require.NotNil(t, row.SelectionHeadGeneration)
	assert.EqualValues(t, 9007199254740993, *row.SelectionHeadGeneration)
	require.NotNil(t, row.Inference)
	assert.Equal(t, "served-model", row.Inference.ServedTarget.CatalogID)
	assert.Nil(t, (<-repo.contexts).Value(contextProbeKey{}))
}

func TestPolicySnapshotOwnsTrainingDeltaAndPreservesLargeInteger(t *testing.T) {
	workers := testObservationWorkers(t)
	messages := []router.ConversationMessage{{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "read_file", InputKeys: []string{"path"}, InputJSON: `{"path":"file.go"}`}}}}
	payload := map[string]any{"training_allowed": true, "selection_head_generation": int64(9007199254740993), "training_conversation_delta": messages}
	snapshots := make(chan map[string]any, 1)
	submitObservation(workers.Remote, observability.WorkFeedback, observability.FromContext(context.Background()), payload, policyPayloadBound(payload), time.Second, func(_ context.Context, p map[string]any) error { snapshots <- p; return nil })
	messages[0].ToolCalls[0].InputKeys[0] = "secret"
	messages[0].ToolCalls[0].InputJSON = "mutated"
	payload["training_allowed"] = false
	got := <-snapshots
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{"training_allowed":true,"selection_head_generation":9007199254740993,"training_conversation_delta":[{"role":"assistant","tool_calls":[{"name":"read_file","input_keys":["path"],"input_json":"{\"path\":\"file.go\"}"}]}]}`, string(encoded))
}

func TestTelemetryPayloadBoundCoversEveryVariableField(t *testing.T) {
	oversizeField := func(value reflect.Value) {
		switch value.Kind() {
		case reflect.String:
			value.SetString(strings.Repeat("x", observability.MaxWorkPayloadBytes))
		case reflect.Slice:
			if value.Type().Elem().Kind() == reflect.String {
				value.Set(reflect.MakeSlice(value.Type(), 1, 1))
				value.Index(0).SetString(strings.Repeat("x", observability.MaxWorkPayloadBytes))
			} else {
				value.Set(reflect.MakeSlice(value.Type(), observability.MaxWorkPayloadBytes, observability.MaxWorkPayloadBytes))
			}
		default:
			return
		}
	}
	typ := reflect.TypeOf(InsertTelemetryParams{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.String && field.Type.Kind() != reflect.Slice {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			var p InsertTelemetryParams
			oversizeField(reflect.ValueOf(&p).Elem().Field(i))
			assert.Greater(t, telemetryPayloadBound(p), observability.MaxWorkPayloadBytes)
		})
	}
}
