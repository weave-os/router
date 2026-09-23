package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func maxSubscriberContext(t *testing.T) context.Context {
	t.Setenv(MaxPlanRosterPinsEnabledEnv, "true")
	return entitlement.WithProductScope(context.Background(), entitlement.PlanMax)
}

func TestMaxSubscriberPinsEveryClusterToTheOpenWeightRoster(t *testing.T) {
	overrides := clusterArmOverridesForRequest(maxSubscriberContext(t))

	require.Equal(t, maxPlanClusterPins, overrides)
}

func TestPinsApplyOnlyToMaxSubscribers(t *testing.T) {
	t.Setenv(MaxPlanRosterPinsEnabledEnv, "true")

	for _, ctx := range []context.Context{
		context.Background(),
		entitlement.WithProductScope(context.Background(), entitlement.PlanBoost),
	} {
		assert.Nil(t, clusterArmOverridesForRequest(ctx))
	}
}

func TestPinsAreInertUntilTheEnvironmentEnablesThem(t *testing.T) {
	ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanMax)

	assert.Nil(t, clusterArmOverridesForRequest(ctx))
}

// The org's own per-key list is an admin control, so the pins compose with it
// exactly as a user's selection does rather than re-admitting what it removed.
func TestOrgClusterListStillNarrowsTheMaxPins(t *testing.T) {
	ctx := context.WithValue(
		maxSubscriberContext(t),
		ClusterModelListsContextKey{},
		map[string][]string{"high": {"z-ai/glm-5.3-flash"}},
	)

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, clusterArmOverridesForRequest(ctx)["high"])
}

// The pins name catalog IDs; an unknown one silently never binds, so the only
// defence against a typo is checking them against the catalog.
func TestPinnedModelsAreCatalogueModelsMaxMayServe(t *testing.T) {
	boundary := entitlement.ModelBoundaryFor(entitlement.PlanMax)
	for cluster, catalogIDs := range maxPlanClusterPins {
		for _, catalogID := range catalogIDs {
			model, known := catalog.ByID(catalogID)
			require.Truef(t, known, "cluster %q pins unknown catalog model %q", cluster, catalogID)
			assert.Truef(t, boundary.PermitsSource(model.Source), "cluster %q pins %q, which Max may not serve", cluster, catalogID)
		}
	}
}
