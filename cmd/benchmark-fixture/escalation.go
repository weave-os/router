package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/llmescalation"
)

func rejectUnknownEscalationVariables(environ []string) error {
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, escalationVariablePrefix) && name != escalationSelectVariable && !slices.Contains(escalationOptionVariables, name) {
			return fmt.Errorf("unsupported fixture escalation variable %s", name)
		}
	}
	return nil
}

func escalationRequestFromEnv(lookup func(string) (string, bool)) (escalationRequest, string, bool, error) {
	selected, _ := lookup(escalationSelectVariable)
	if selected == "" {
		for _, name := range escalationOptionVariables {
			if _, found := lookup(name); found {
				return escalationRequest{}, "", false, fmt.Errorf("%s requires %s=%s", name, escalationSelectVariable, escalationSelectEnabled)
			}
		}
		return escalationRequest{}, "", false, nil
	}
	if selected != escalationSelectEnabled {
		return escalationRequest{}, "", false, fmt.Errorf("%s must be %s when set", escalationSelectVariable, escalationSelectEnabled)
	}
	for _, name := range escalationOptionVariables {
		if value, found := lookup(name); !found || value == "" {
			return escalationRequest{}, "", false, fmt.Errorf("%s=%s requires a non-empty %s", escalationSelectVariable, escalationSelectEnabled, name)
		}
	}
	active, _ := lookup(escalationActiveVariable)
	shadow, _ := lookup(escalationShadowVariable)
	cadence, _ := lookup(escalationCadenceVariable)
	evidencePath, _ := lookup(escalationEvidenceVariable)
	parsedCadence, err := strconv.Atoi(cadence)
	if err != nil {
		return escalationRequest{}, "", false, fmt.Errorf("%s must be an integer", escalationCadenceVariable)
	}
	request := escalationRequest{Active: flags.EscalationClassifier(active), Shadow: flags.EscalationClassifier(shadow), Cadence: parsedCadence}
	if !filepath.IsAbs(evidencePath) {
		return escalationRequest{}, "", false, fmt.Errorf("%s must be absolute", escalationEvidenceVariable)
	}
	return request, evidencePath, true, nil
}

func selectFixtureEscalation(ctx context.Context, databaseURL, installationID string, request escalationRequest) (fixtureEscalation, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fixtureEscalation{}, errors.New("escalation selection could not connect to disposable database")
	}
	defer pool.Close()
	return applyEscalationSelection(ctx, postgres.NewLLMEscalationRepo(pool), installationID, request)
}

func applyEscalationSelection(ctx context.Context, store llmescalation.ConfigurationStore, installationID string, request escalationRequest) (fixtureEscalation, error) {
	current, err := store.GetSelection(ctx, installationID)
	if err != nil {
		return fixtureEscalation{}, fmt.Errorf("escalation selection could not be read: %w", err)
	}
	applied, err := store.SetSelection(ctx, installationID, llmescalation.SelectionUpdate{Active: request.Active, Shadow: request.Shadow, Cadence: request.Cadence, Epoch: current.Epoch})
	if errors.Is(err, flags.ErrInvalidValue) {
		return fixtureEscalation{}, fmt.Errorf("escalation selection active=%q shadow=%q cadence=%d rejected by router flags: %w", request.Active, request.Shadow, request.Cadence, err)
	}
	if errors.Is(err, llmescalation.ErrConfigurationConflict) {
		return fixtureEscalation{}, fmt.Errorf("escalation selection changed during fixture setup: %w", err)
	}
	if err != nil {
		return fixtureEscalation{}, fmt.Errorf("escalation selection could not be saved: %w", err)
	}
	if applied.Active != request.Active || applied.Shadow != request.Shadow || applied.Cadence != request.Cadence {
		return fixtureEscalation{}, fmt.Errorf("router saved escalation active=%q shadow=%q cadence=%d instead of the requested active=%q shadow=%q cadence=%d", applied.Active, applied.Shadow, applied.Cadence, request.Active, request.Shadow, request.Cadence)
	}
	readback, err := store.GetSelection(ctx, installationID)
	if err != nil {
		return fixtureEscalation{}, fmt.Errorf("escalation selection could not be read back: %w", err)
	}
	if readback.Active != applied.Active || readback.Shadow != applied.Shadow || readback.Cadence != applied.Cadence || readback.Epoch != applied.Epoch {
		return fixtureEscalation{}, errors.New("escalation selection readback differs from the saved selection")
	}
	return fixtureEscalation{SchemaVersion: escalationSchema, InstallationID: installationID, Active: applied.Active, Shadow: applied.Shadow, Cadence: applied.Cadence, PreviousEpoch: current.Epoch, Epoch: applied.Epoch}, nil
}
