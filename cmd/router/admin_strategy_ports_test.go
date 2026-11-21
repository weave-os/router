package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/rosterdata"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

type fixtureDistributionSource struct{ name string }

func (fixtureDistributionSource) DistributionRoster(context.Context, router.Strategy) (router.DistributionRoster, error) {
	return nil, nil
}

var errFixtureRosterUnavailable = errors.New("fixture roster unavailable")

type unavailableDistributionRosters struct{}

func (unavailableDistributionRosters) DistributionRoster(context.Context, router.Strategy) (*rosterdata.Roster, error) {
	return nil, errFixtureRosterUnavailable
}

func TestHMMRoutingDistributionReadsInjectedRosters(t *testing.T) {
	_, err := hmmRoutingDistribution(unavailableDistributionRosters{}).DistributionRoster(context.Background(), router.StrategyHMMEmbedding)
	assert.ErrorIs(t, err, errFixtureRosterUnavailable, "the distribution route reads rosters from the injected source")
}

func TestWithHMMAdminPortsSelfHostedKeepsConfiguredDistribution(t *testing.T) {
	configured := fixtureDistributionSource{name: "configured"}
	features := withHMMAdminPorts(server.Features{SubscriptionPoolsEnabled: true}, nil, configured)

	assert.Equal(t, configured, features.RoutingDistribution, "without serving admission the configured distribution source backs the route")
	assert.True(t, features.SubscriptionPoolsEnabled, "existing features are preserved")
	assertHMMAdminPortsWired(t, features)
}

func TestWithHMMAdminPortsServingAdmissionUsesAdmittedRosters(t *testing.T) {
	configured := fixtureDistributionSource{name: "configured"}
	features := withHMMAdminPorts(server.Features{}, &middleware.ServingAdmissionConfig{}, configured)

	assert.Equal(t, selection.DistributionSource{Rosters: policyregistry.AdmittedRosterSource{}}, features.RoutingDistribution,
		"with serving admission the distribution reads the admitted roster, not the configured source")
	assertHMMAdminPortsWired(t, features)
}

func TestWithHMMAdminPortsLeavesNilDistributionUnwired(t *testing.T) {
	features := withHMMAdminPorts(server.Features{}, nil, nil)
	assert.Nil(t, features.RoutingDistribution)
}

func assertHMMAdminPortsWired(t *testing.T, features server.Features) {
	t.Helper()
	require.NotNil(t, features.RosterCatalogID)
	const arm = "anthropic/claude-opus-5:xhigh"
	assert.Equal(t, "claude-opus-5", features.RosterCatalogID(arm), "the catalog projection strips the effort suffix")
	assert.Equal(t, selection.TaskDomainPreviewer{}, features.TaskDomainPreview)
}
