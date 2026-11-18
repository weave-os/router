package policyregistry_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

type payloadLoader struct {
	*fakeLoader
	payload []byte
}

func (l *payloadLoader) ReadPolicy(context.Context, policyregistry.ObjectRef) (*selectionpolicy.Policy, error) {
	return selectionpolicy.Admit(l.payload)
}

func newPayloadLoader(t *testing.T, payload string) *payloadLoader {
	t.Helper()
	return &payloadLoader{fakeLoader: validLoader(t), payload: []byte(payload)}
}

func newAdmissionManager(t *testing.T, loader policyregistry.Loader) *policyregistry.Manager {
	t.Helper()
	manager, err := policyregistry.NewManager(loader, policyregistry.EnvironmentStaging, policyregistry.LaneStable,
		func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
			return map[router.Strategy]router.Router{router.StrategyHMM: fixedRouter{model: "admitted"}}, nil
		}, slog.Default())
	require.NoError(t, err)
	return manager
}

func TestManagerColdBootActivatesAdmittedPayloadWithItsDigest(t *testing.T) {
	loader := newPayloadLoader(t, directoryPolicyJSON)
	manager := newAdmissionManager(t, loader)

	require.NoError(t, manager.Refresh(context.Background()))
	active := manager.Active()
	require.NotNil(t, active)
	assert.Equal(t, policyregistry.Digest([]byte(directoryPolicyJSON)), active.Policy.SHA256())
	assert.Equal(t, int64(1), manager.Status().ActiveHeadGeneration)
}

func TestManagerColdBootFailsClosedWhenAdmissionRejectsPayload(t *testing.T) {
	loader := newPayloadLoader(t, strings.ReplaceAll(directoryPolicyJSON, "openai/gpt-5.6-sol", "madeup/not-a-model"))
	manager := newAdmissionManager(t, loader)

	require.ErrorContains(t, manager.Refresh(context.Background()), "unknown_catalog_model")
	assert.Nil(t, manager.Active())
	assert.Equal(t, int64(1), manager.Status().RejectedGeneration)
}

func TestManagerWarmRefreshRetainsActiveSnapshotWhenAdmissionRejectsPayload(t *testing.T) {
	loader := newPayloadLoader(t, directoryPolicyJSON)
	manager := newAdmissionManager(t, loader)
	require.NoError(t, manager.Refresh(context.Background()))
	first := manager.Active()
	require.NotNil(t, first)

	loader.head.Generation = 2
	loader.payload = []byte(strings.Replace(directoryPolicyJSON, `"class_order"`, `"unexpected":true,"class_order"`, 1))
	require.Error(t, manager.Refresh(context.Background()))
	assert.Same(t, first, manager.Active())
	assert.Equal(t, int64(1), manager.Status().ActiveHeadGeneration)
	assert.Equal(t, int64(2), manager.Status().RejectedGeneration)

	loader.head.Generation = 3
	loader.payload = []byte(directoryPolicyJSON)
	require.NoError(t, manager.Refresh(context.Background()))
	assert.Equal(t, int64(3), manager.Status().ActiveHeadGeneration)
}
