package conversation_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeForceResolver struct {
	known   bool
	effort  string
	refusal string
}

func (f fakeForceResolver) Resolve(model string) (string, string, bool, string) {
	return "canonical-" + model, "provider-a", f.known, f.effort
}

func (fakeForceResolver) Suggest(string) string { return "closest-model" }

func (f fakeForceResolver) Binding(context.Context, string, string) (string, string, bool) {
	if f.refusal != "" {
		return "", f.refusal, true
	}
	return "provider-b", "", false
}

type fakeForcePins struct {
	sets      []string
	threadKey string
	clears    int
	err       error
}

func (f *fakeForcePins) Set(_ context.Context, _ [sessionpin.SessionKeyLen]byte, _ uuid.UUID, model, provider, _, forcedThreadKey string) error {
	f.sets = append(f.sets, model+"@"+provider)
	f.threadKey = forcedThreadKey
	return f.err
}

func (f *fakeForcePins) Clear(context.Context, uuid.UUID, [sessionpin.SessionKeyLen]byte) error {
	f.clears++
	return f.err
}

type fakeForceControls struct {
	legacyClears int
	boundaries   []string
	legacyErr    error
}

func (f *fakeForceControls) ClearLegacyPins(context.Context, uuid.UUID, [sessionpin.SessionKeyLen]byte) error {
	f.legacyClears++
	return f.legacyErr
}

func (f *fakeForceControls) RecordCommandBoundary(_ context.Context, _ uuid.UUID, _ [sessionpin.SessionKeyLen]byte, commandID string) {
	f.boundaries = append(f.boundaries, commandID)
}

func forceRequest(cmd translate.ForceModelResult) conversation.ForceModelRequest {
	return conversation.ForceModelRequest{
		Command:              cmd,
		InstallationID:       uuid.New(),
		ThreadSessionKey:     [sessionpin.SessionKeyLen]byte{0xab},
		ForceModelSessionKey: [sessionpin.SessionKeyLen]byte{0xcd},
		CommandID:            "command-1",
		UnforceCommand:       "/unforce-model",
	}
}

func TestForceModelCommandAppliesPin(t *testing.T) {
	pins := &fakeForcePins{}
	controls := &fakeForceControls{}
	out, err := conversation.ForceModelCommand{Resolver: fakeForceResolver{known: true, effort: "high"}, Pins: pins, Controls: controls}.
		Apply(context.Background(), forceRequest(translate.ForceModelResult{Model: "m"}))
	require.NoError(t, err)
	assert.Empty(t, out.Rejection)
	assert.Equal(t, "canonical-m:high", out.ShownModel)
	assert.Equal(t, translate.CommandPin{Transition: translate.PinApplied, Model: "canonical-m", Provider: "provider-b", Effort: "high"}, out.Pin)
	assert.Equal(t, "/unforce-model", out.ClearCommand)
	assert.Equal(t, []string{"canonical-m@provider-b"}, pins.sets)
	assert.Empty(t, pins.threadKey)
	assert.Equal(t, 1, controls.legacyClears)
	assert.Equal(t, []string{"command-1"}, controls.boundaries)
}

func TestForceParentScopesPinToThread(t *testing.T) {
	pins := &fakeForcePins{}
	out, err := conversation.ForceModelCommand{Resolver: fakeForceResolver{known: true}, Pins: pins, Controls: &fakeForceControls{}}.
		Apply(context.Background(), forceRequest(translate.ForceModelResult{Model: "m", ParentOnly: true}))
	require.NoError(t, err)
	assert.True(t, out.ParentOnly)
	assert.Equal(t, "ab000000000000000000000000000000", pins.threadKey)
}

func TestForceModelCommandRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		resolver fakeForceResolver
		want     conversation.ForceModelOutcome
	}{
		"unknown":  {resolver: fakeForceResolver{}, want: conversation.ForceModelOutcome{Rejection: conversation.ForceModelUnknown, InputModel: "m", Suggestion: "closest-model"}},
		"excluded": {resolver: fakeForceResolver{known: true, refusal: "not allowed"}, want: conversation.ForceModelOutcome{Rejection: conversation.ForceModelExcluded, Reason: "not allowed"}},
	} {
		t.Run(name, func(t *testing.T) {
			pins := &fakeForcePins{}
			controls := &fakeForceControls{}
			out, err := conversation.ForceModelCommand{Resolver: tc.resolver, Pins: pins, Controls: controls}.
				Apply(context.Background(), forceRequest(translate.ForceModelResult{Model: "m"}))
			require.NoError(t, err)
			assert.Equal(t, tc.want, out)
			assert.Empty(t, pins.sets)
			assert.Zero(t, controls.legacyClears)
			assert.Empty(t, controls.boundaries)
		})
	}
}

func TestUnforceModelClearsPins(t *testing.T) {
	pins := &fakeForcePins{}
	controls := &fakeForceControls{}
	out, err := conversation.ForceModelCommand{Resolver: fakeForceResolver{}, Pins: pins, Controls: controls}.
		Apply(context.Background(), forceRequest(translate.ForceModelResult{Clear: true}))
	require.NoError(t, err)
	assert.Equal(t, translate.CommandPin{Transition: translate.PinCleared}, out.Pin)
	assert.Equal(t, 1, pins.clears)
	assert.Equal(t, 1, controls.legacyClears)
	assert.Equal(t, []string{"command-1"}, controls.boundaries)
}

func TestForceModelCommandStoreErrorsStopBeforeBoundary(t *testing.T) {
	controls := &fakeForceControls{legacyErr: errors.New("legacy store down")}
	_, err := conversation.ForceModelCommand{Resolver: fakeForceResolver{known: true}, Pins: &fakeForcePins{}, Controls: controls}.
		Apply(context.Background(), forceRequest(translate.ForceModelResult{Model: "m"}))
	require.Error(t, err)
	assert.Empty(t, controls.boundaries)

	controls = &fakeForceControls{}
	_, err = conversation.ForceModelCommand{Resolver: fakeForceResolver{}, Pins: &fakeForcePins{err: errors.New("pin store down")}, Controls: controls}.
		Apply(context.Background(), forceRequest(translate.ForceModelResult{Clear: true}))
	require.Error(t, err)
	assert.Empty(t, controls.boundaries)
}
