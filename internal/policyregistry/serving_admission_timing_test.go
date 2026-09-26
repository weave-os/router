package policyregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// slowServingStore delays every authoritative read so a recorded duration is
// distinguishable from an unrecorded zero.
type slowServingStore struct {
	*servingMemoryStore
	delay time.Duration
}

func (s slowServingStore) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	time.Sleep(s.delay)
	return s.servingMemoryStore.ReadServingState(ctx, target)
}

func (s slowServingStore) ReadServingObject(ctx context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	time.Sleep(s.delay)
	return s.servingMemoryStore.ReadServingObject(ctx, kind, ref)
}

func TestAdmissionTimingsCountEveryAuthoritativeRead(t *testing.T) {
	const delay = 5 * time.Millisecond
	memory := newServingMemoryStore()
	first, second := fixtureSet("timing-first"), fixtureSet("timing-second")
	memory.publish(t, policyregistry.ServingSelectionSets, first)
	memory.publish(t, policyregistry.ServingSelectionSets, second)
	initial, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, first, servingEpoch)
	memory.states[policyregistry.TargetStable] = initial
	store := slowServingStore{servingMemoryStore: memory, delay: delay}
	admission := policyregistry.ServingAdmission{Store: store}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
	now := servingEpoch
	clock := func(context.Context) (time.Time, error) { return now, nil }

	fresh := &policyregistry.AdmissionTimings{}
	binding, err := admission.Decide(context.Background(), policyregistry.SerializedAdmission{Projection: projection, Clock: clock, Timings: fresh})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, fresh.StateRead, delay)
	assert.GreaterOrEqual(t, fresh.SelectionSetRead, delay)
	assert.Equal(t, 1, fresh.SelectionSetReads)

	replacement, _ := activateFixture(t, initial, second, servingEpoch.Add(time.Minute))
	memory.states[policyregistry.TargetStable] = replacement
	now = servingEpoch.Add(2 * time.Minute)
	retained := &policyregistry.AdmissionTimings{}
	_, err = admission.Decide(context.Background(), policyregistry.SerializedAdmission{Projection: projection, Previous: &binding, Clock: clock, Timings: retained})
	require.NoError(t, err)
	assert.Equal(t, 2, retained.SelectionSetReads)
	assert.GreaterOrEqual(t, retained.SelectionSetRead, 2*delay)
	assert.GreaterOrEqual(t, retained.StateRead, delay)

	// A nil pointer is the documented opt-out and must not change the decision.
	unmeasured, err := admission.Decide(context.Background(), policyregistry.SerializedAdmission{Projection: projection, Previous: &binding, Clock: clock})
	require.NoError(t, err)
	assert.Equal(t, binding.ActivationID, unmeasured.ActivationID)
}
