package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/sqlc"
)

const fakeInstallationID = "8d0f7a52-7b0e-4a39-9a43-5f1d6f0c2b11"

func TestCanonicalManagedFixture(t *testing.T) {
	dsn := os.Getenv("HARNESS_FIXTURE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly owned disposable Postgres and local registry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	external := uuid.NewString()
	name := "router-recipe-test"
	creator := "fixture-test"
	installation, err := sqlc.New(pool).CreateModelRouterInstallation(ctx, sqlc.CreateModelRouterInstallationParams{ExternalID: external, Name: name, CreatedBy: &creator})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provisionManagedFixture(ctx, dsn, installation.ID.String(), external)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.ManagedAdmissionReady || binding.SubjectID == "" || binding.RouterKey == "" {
		t.Fatal("canonical fixture did not establish managed readiness")
	}
	repo := postgres.NewLLMEscalationRepo(pool)
	evidence, err := applyEscalationSelection(ctx, repo, installation.ID.String(), escalationRequest{Active: flags.EscalationClassifierXGB, Shadow: flags.EscalationClassifierNone, Cadence: 5})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.InstallationID != installation.ID.String() || evidence.Active != flags.EscalationClassifierXGB || evidence.Shadow != flags.EscalationClassifierNone || evidence.Cadence != 5 || evidence.Epoch != evidence.PreviousEpoch+1 {
		t.Fatalf("canonical escalation selection evidence = %+v", evidence)
	}
	_, err = applyEscalationSelection(ctx, repo, installation.ID.String(), escalationRequest{Active: "not_a_classifier", Shadow: flags.EscalationClassifierNone, Cadence: 5})
	if !errors.Is(err, flags.ErrInvalidValue) {
		t.Fatalf("unknown classifier error = %v, want flags.ErrInvalidValue", err)
	}
}

// selectedEscalation is the complete environment the Python composer writes,
// with overrides applied; an empty override value removes that variable.
func selectedEscalation(overrides map[string]string) map[string]string {
	environment := map[string]string{
		"HARNESS_FIXTURE_ESCALATION_SELECT":     "1",
		"HARNESS_FIXTURE_ESCALATION_CLASSIFIER": "xgb",
		"HARNESS_FIXTURE_ESCALATION_SHADOW":     "none",
		"HARNESS_FIXTURE_ESCALATION_CADENCE":    "5",
		"HARNESS_FIXTURE_ESCALATION_PATH":       "/state/fixture-escalation.json",
	}
	for name, value := range overrides {
		if value == "" {
			delete(environment, name)
			continue
		}
		environment[name] = value
	}
	return environment
}

func TestEscalationRequestFromEnv(t *testing.T) {
	cases := []struct {
		name         string
		environment  map[string]string
		wantRequest  escalationRequest
		wantPath     string
		wantEnabled  bool
		wantErrorFor string
	}{
		{name: "disabled when select is unset", environment: map[string]string{}},
		{name: "disabled when select is empty", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_SELECT": ""}},
		{
			name:        "reads the complete composed selection",
			environment: selectedEscalation(nil),
			wantRequest: escalationRequest{Active: "xgb", Shadow: "none", Cadence: 5},
			wantPath:    "/state/fixture-escalation.json",
			wantEnabled: true,
		},
		{
			name: "passes an unvalidated classifier name and cadence through",
			environment: selectedEscalation(map[string]string{
				"HARNESS_FIXTURE_ESCALATION_CLASSIFIER": "llm_escalation",
				"HARNESS_FIXTURE_ESCALATION_SHADOW":     "xgb",
				"HARNESS_FIXTURE_ESCALATION_CADENCE":    "4",
				"HARNESS_FIXTURE_ESCALATION_PATH":       "/state/arm-a/escalation.json",
			}),
			wantRequest: escalationRequest{Active: "llm_escalation", Shadow: "xgb", Cadence: 4},
			wantPath:    "/state/arm-a/escalation.json",
			wantEnabled: true,
		},
		{
			name:        "leaves cadence range to the router",
			environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_CADENCE": "9"}),
			wantRequest: escalationRequest{Active: "xgb", Shadow: "none", Cadence: 9},
			wantPath:    "/state/fixture-escalation.json",
			wantEnabled: true,
		},
		{name: "has no default classifier", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_SELECT": "1"}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CLASSIFIER"},
		{name: "has no default shadow", environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_SHADOW": ""}), wantErrorFor: "HARNESS_FIXTURE_ESCALATION_SHADOW"},
		{name: "has no default cadence", environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_CADENCE": ""}), wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CADENCE"},
		{name: "has no default evidence path", environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_PATH": ""}), wantErrorFor: "HARNESS_FIXTURE_ESCALATION_PATH"},
		{name: "rejects a classifier without select", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_CLASSIFIER": "xgb"}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CLASSIFIER"},
		{name: "rejects an empty path without select", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_PATH": ""}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_PATH"},
		{name: "rejects select values other than 1", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_SELECT": "true"}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_SELECT"},
		{name: "rejects an empty classifier", environment: map[string]string{"HARNESS_FIXTURE_ESCALATION_SELECT": "1", "HARNESS_FIXTURE_ESCALATION_CLASSIFIER": "", "HARNESS_FIXTURE_ESCALATION_SHADOW": "none", "HARNESS_FIXTURE_ESCALATION_CADENCE": "5", "HARNESS_FIXTURE_ESCALATION_PATH": "/state/e.json"}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CLASSIFIER"},
		{name: "rejects a non-integer cadence", environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_CADENCE": "x"}), wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CADENCE"},
		{name: "rejects a relative evidence path", environment: selectedEscalation(map[string]string{"HARNESS_FIXTURE_ESCALATION_PATH": "state/fixture-escalation.json"}), wantErrorFor: "HARNESS_FIXTURE_ESCALATION_PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				value, found := tc.environment[name]
				return value, found
			}
			request, path, enabled, err := escalationRequestFromEnv(lookup)
			if tc.wantErrorFor != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrorFor) {
					t.Fatalf("error = %v, want one naming %s", err, tc.wantErrorFor)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if request != tc.wantRequest || path != tc.wantPath || enabled != tc.wantEnabled {
				t.Fatalf("got (%+v, %q, %t), want (%+v, %q, %t)", request, path, enabled, tc.wantRequest, tc.wantPath, tc.wantEnabled)
			}
		})
	}
}

func TestRejectUnknownEscalationVariables(t *testing.T) {
	cases := []struct {
		name         string
		environ      []string
		wantErrorFor string
	}{
		{name: "accepts every declared option", environ: []string{"HARNESS_FIXTURE_ESCALATION_SELECT=1", "HARNESS_FIXTURE_ESCALATION_CLASSIFIER=xgb", "HARNESS_FIXTURE_ESCALATION_SHADOW=none", "HARNESS_FIXTURE_ESCALATION_CADENCE=5", "HARNESS_FIXTURE_ESCALATION_PATH=/state/e.json", "HARNESS_FIXTURE_DB_HOST=postgres"}},
		{name: "rejects a misspelled classifier option", environ: []string{"HARNESS_FIXTURE_ESCALATION_SELECT=1", "HARNESS_FIXTURE_ESCALATION_CLASIFIER=llm_escalation"}, wantErrorFor: "HARNESS_FIXTURE_ESCALATION_CLASIFIER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectUnknownEscalationVariables(tc.environ)
			if tc.wantErrorFor == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErrorFor != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrorFor)) {
				t.Fatalf("error = %v, want one naming %s", err, tc.wantErrorFor)
			}
		})
	}
}

// recordingSelectionStore saves what it is asked to, bumping the epoch like the
// canonical repository, unless a test overrides the stored outcome.
type recordingSelectionStore struct {
	stored          llmescalation.Selection
	saveErr         error
	savedActive     flags.EscalationClassifier
	readbackDrift   int
	receivedUpdates []llmescalation.SelectionUpdate
}

func (s *recordingSelectionStore) GetSelection(_ context.Context, installation string) (llmescalation.Selection, error) {
	if installation != s.stored.InstallationID {
		return llmescalation.Selection{}, llmescalation.ErrInstallationNotFound
	}
	return s.stored, nil
}

func (s *recordingSelectionStore) SetSelection(_ context.Context, installation string, update llmescalation.SelectionUpdate) (llmescalation.Selection, error) {
	s.receivedUpdates = append(s.receivedUpdates, update)
	if s.saveErr != nil {
		return llmescalation.Selection{}, s.saveErr
	}
	saved := llmescalation.Selection{InstallationID: installation, Active: update.Active, Shadow: update.Shadow, Cadence: update.Cadence, Epoch: update.Epoch + 1}
	if s.savedActive != "" {
		saved.Active = s.savedActive
	}
	s.stored = saved
	s.stored.Epoch += s.readbackDrift
	return saved, nil
}

func legacySelection() llmescalation.Selection {
	return llmescalation.Selection{InstallationID: fakeInstallationID, Active: flags.EscalationClassifierNone, Shadow: flags.EscalationClassifierNone, Cadence: 3, Epoch: 7}
}

func TestApplyEscalationSelection(t *testing.T) {
	request := escalationRequest{Active: flags.EscalationClassifierLLM, Shadow: flags.EscalationClassifierXGB, Cadence: 4}

	t.Run("saves against the current epoch and records both epochs", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection()}
		evidence, err := applyEscalationSelection(context.Background(), store, fakeInstallationID, request)
		if err != nil {
			t.Fatal(err)
		}
		wantUpdate := llmescalation.SelectionUpdate{Active: "llm_escalation", Shadow: "xgb", Cadence: 4, Epoch: 7}
		if len(store.receivedUpdates) != 1 || store.receivedUpdates[0] != wantUpdate {
			t.Fatalf("updates = %+v, want exactly %+v", store.receivedUpdates, wantUpdate)
		}
		want := fixtureEscalation{SchemaVersion: "router_benchmark_fixture_escalation.v1", InstallationID: fakeInstallationID, Active: "llm_escalation", Shadow: "xgb", Cadence: 4, PreviousEpoch: 7, Epoch: 8}
		if evidence != want {
			t.Fatalf("evidence = %+v, want %+v", evidence, want)
		}
	})

	t.Run("propagates a configuration conflict", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection(), saveErr: llmescalation.ErrConfigurationConflict}
		_, err := applyEscalationSelection(context.Background(), store, fakeInstallationID, request)
		if !errors.Is(err, llmescalation.ErrConfigurationConflict) {
			t.Fatalf("error = %v, want ErrConfigurationConflict", err)
		}
	})

	t.Run("rejects a store that saved a different active classifier", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection(), savedActive: flags.EscalationClassifierSwitchyard}
		_, err := applyEscalationSelection(context.Background(), store, fakeInstallationID, request)
		if err == nil || !strings.Contains(err.Error(), `active="switchyard_llm_v1"`) {
			t.Fatalf("error = %v, want one naming the saved classifier", err)
		}
	})

	t.Run("rejects a readback that differs from the save", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection(), readbackDrift: 1}
		_, err := applyEscalationSelection(context.Background(), store, fakeInstallationID, request)
		if err == nil || !strings.Contains(err.Error(), "readback") {
			t.Fatalf("error = %v, want a readback mismatch", err)
		}
	})

	t.Run("names the classifier the router flags rejected", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection(), saveErr: fmt.Errorf("%w: unsupported escalation classifier", flags.ErrInvalidValue)}
		_, err := applyEscalationSelection(context.Background(), store, fakeInstallationID, escalationRequest{Active: "async_judge_v2", Shadow: flags.EscalationClassifierNone, Cadence: 5})
		if !errors.Is(err, flags.ErrInvalidValue) || !strings.Contains(err.Error(), `active="async_judge_v2"`) {
			t.Fatalf("error = %v, want flags.ErrInvalidValue naming async_judge_v2", err)
		}
	})

	t.Run("fails before saving when the installation is missing", func(t *testing.T) {
		store := &recordingSelectionStore{stored: legacySelection()}
		_, err := applyEscalationSelection(context.Background(), store, uuid.NewString(), request)
		if !errors.Is(err, llmescalation.ErrInstallationNotFound) || len(store.receivedUpdates) != 0 {
			t.Fatalf("error = %v with %d updates, want ErrInstallationNotFound and no save", err, len(store.receivedUpdates))
		}
	})
}
