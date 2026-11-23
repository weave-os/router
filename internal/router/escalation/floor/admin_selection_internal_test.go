package floor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/llmescalation"
)

type escalationConfigurationStub struct {
	selection llmescalation.Selection
	updates   int
}

func (s *escalationConfigurationStub) GetSelection(context.Context, string) (llmescalation.Selection, error) {
	return s.selection, nil
}

func (s *escalationConfigurationStub) SetSelection(_ context.Context, _ string, update llmescalation.SelectionUpdate) (llmescalation.Selection, error) {
	s.updates++
	s.selection.Active, s.selection.Shadow, s.selection.Cadence = update.Active, update.Shadow, update.Cadence
	return s.selection, nil
}

func TestLLMEscalationConfigurationGatesActiveRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	invalidations := 0
	service := &Admin{SwitchyardJudge: &blockingEscalationJudge{}, Configuration: configuration, InvalidateSelection: func(string) { invalidations++ }, SwitchyardActive: false}

	_, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierSwitchyard, Cadence: 3, Epoch: 2})
	require.ErrorIs(t, err, ErrEscalationJudgeUnavailable)
	require.Zero(t, configuration.updates)

	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierXGB, Shadow: flags.EscalationClassifierSwitchyard, Cadence: 3, Epoch: 2})
	require.NoError(t, err)
	require.Equal(t, flags.EscalationClassifierSwitchyard, selection.Shadow)
	require.Equal(t, 1, configuration.updates)
	require.Equal(t, 1, invalidations)
}

func TestQwenEscalationSelectionRequiresDeployedJudge(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	service := &Admin{SwitchyardJudge: &blockingEscalationJudge{}, Configuration: configuration, InvalidateSelection: nil, SwitchyardActive: true}
	update := llmescalation.SelectionUpdate{Active: flags.EscalationClassifierLLM, Cadence: 3, Epoch: 2}

	_, err := service.UpdateEscalationSelection(context.Background(), "installation", update)
	require.ErrorIs(t, err, ErrEscalationJudgeUnavailable)
	require.Zero(t, configuration.updates)

	service.QwenJudge = &blockingEscalationJudge{}
	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", update)
	require.NoError(t, err)
	require.Equal(t, flags.EscalationClassifierLLM, selection.Active)
	require.True(t, selection.Ready)
	require.Equal(t, 1, configuration.updates)
}

func TestSwitchyardEscalationReadinessReflectsDisabledRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Active: flags.EscalationClassifierSwitchyard}}
	service := &Admin{SwitchyardJudge: &blockingEscalationJudge{}, Configuration: configuration, InvalidateSelection: nil, SwitchyardActive: false}
	selection, err := service.EscalationSelection(context.Background(), "installation")
	require.NoError(t, err)
	require.False(t, selection.Ready)
	require.Equal(t, "active rollout is disabled", selection.UnavailableReason)
}

func TestQwenEscalationSelectionDoesNotRequireSwitchyardActiveRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	service := &Admin{QwenJudge: &blockingEscalationJudge{}, Configuration: configuration, SwitchyardActive: false}
	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierLLM, Cadence: 3, Epoch: 2})
	require.NoError(t, err)
	require.True(t, selection.Ready)
}
