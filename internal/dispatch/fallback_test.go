package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

var errPoolExhausted = errors.New("subscription pool exhausted")

type fakeCoverage struct {
	stateModels    bool
	covered        bool
	paidForbidden  bool
	attempts       int
	apiOnlyEntered bool
	intendedModel  string
}

func (c *fakeCoverage) Begin(ctx context.Context, _ dispatch.FailoverInputs) context.Context {
	return ctx
}
func (c *fakeCoverage) StateModelsEnabled(context.Context) bool         { return c.stateModels }
func (c *fakeCoverage) CoversWhenInactive(context.Context, string) bool { return c.covered }
func (c *fakeCoverage) OverflowEnabled(context.Context) bool            { return false }
func (c *fakeCoverage) AttemptOnly(context.Context) bool                { return false }
func (c *fakeCoverage) APIOnly(context.Context) bool                    { return false }
func (c *fakeCoverage) WithAttempt(ctx context.Context) context.Context {
	c.attempts++
	return ctx
}
func (c *fakeCoverage) WithAPIOnly(ctx context.Context) context.Context {
	c.apiOnlyEntered = true
	return ctx
}
func (c *fakeCoverage) PaidFallbackForbidden(context.Context) bool { return c.paidForbidden }
func (c *fakeCoverage) RotationBudgetSpent(context.Context) bool   { return false }
func (c *fakeCoverage) NoteIntendedModel(_ context.Context, model string) {
	c.intendedModel = model
}
func (c *fakeCoverage) EligibilityRefusal(context.Context, string) error { return nil }
func (c *fakeCoverage) PoolExhausted(context.Context) error              { return errPoolExhausted }

type fakeFallbackCredentials struct {
	resolved []string
	cleared  int
}

func (f *fakeFallbackCredentials) ResolveCredentials(ctx context.Context, provider, _ string, _ http.Header) context.Context {
	f.resolved = append(f.resolved, provider)
	return ctx
}
func (f *fakeFallbackCredentials) ClearCredentials(ctx context.Context) context.Context {
	f.cleared++
	return ctx
}
func (f *fakeFallbackCredentials) InjectCredentials(ctx context.Context, _, _ string) context.Context {
	return ctx
}

type noBindings struct{}

func (noBindings) ResolveBindingsForDispatch(context.Context, router.Decision) []catalog.ProviderBinding {
	return nil
}

func fallbackWith(coverage *fakeCoverage, creds *fakeFallbackCredentials) dispatch.Fallback {
	return dispatch.Fallback{Coverage: coverage, Credentials: creds, Bindings: noBindings{}}
}

func TestFallbackRefusesDispatchWithoutPurpose(t *testing.T) {
	_, err := fallbackWith(&fakeCoverage{}, &fakeFallbackCredentials{}).Dispatch(context.Background(), dispatch.FailoverInputs{})
	require.ErrorIs(t, err, dispatch.ErrDispatchWithoutPurpose)
}

func TestFallbackRefusesModelOutsideInactiveSubscriptionCoverage(t *testing.T) {
	_, err := fallbackWith(&fakeCoverage{stateModels: true}, &fakeFallbackCredentials{}).Dispatch(context.Background(), dispatch.FailoverInputs{})
	require.ErrorIs(t, err, router.ErrAllowlistEmptiesPool)
}

func TestFallbackFlushesForcedModelWithoutBindings(t *testing.T) {
	var flushed error
	in := dispatch.FailoverInputs{
		Purpose:         inference.Purpose("messages"),
		InitialDecision: router.Decision{Model: "model-a", Provider: "provider-a", Reason: router.ReasonUserForceModel},
		Writer:          httptest.NewRecorder(),
		FlushErr:        func(_ http.ResponseWriter, err error) { flushed = err },
	}
	_, err := fallbackWith(&fakeCoverage{}, &fakeFallbackCredentials{}).Dispatch(context.Background(), in)

	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.Status)
	assert.Equal(t, err, flushed)
}

func TestFallbackReturnsBadGatewayWithoutBindingsForRoutedModel(t *testing.T) {
	flushed := false
	in := dispatch.FailoverInputs{
		Purpose:  inference.Purpose("messages"),
		FlushErr: func(http.ResponseWriter, error) { flushed = true },
	}
	_, err := fallbackWith(&fakeCoverage{}, &fakeFallbackCredentials{}).Dispatch(context.Background(), in)

	var status *providers.UpstreamStatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusBadGateway, status.Status)
	assert.False(t, flushed)
}

func TestFallbackAlternativesTryEachTargetThenRefuseWhenPaidFallbackForbidden(t *testing.T) {
	coverage := &fakeCoverage{paidForbidden: true}
	creds := &fakeFallbackCredentials{}
	in := dispatch.FailoverInputs{
		InitialDecision:  router.Decision{Model: "model-a", Provider: "provider-a"},
		Alternatives:     []router.Decision{{Model: "model-b", Provider: "provider-b"}},
		BuildAlternative: func(router.Decision) (dispatch.UpstreamAttempt, error) { return nil, nil },
	}
	_, err := fallbackWith(coverage, creds).Dispatch(context.Background(), in)

	require.ErrorIs(t, err, errPoolExhausted)
	assert.Equal(t, "model-a", coverage.intendedModel)
	assert.Equal(t, 2, coverage.attempts)
	assert.Equal(t, 1, creds.cleared)
	assert.Equal(t, []string{"provider-a", "provider-b"}, creds.resolved)
	assert.False(t, coverage.apiOnlyEntered)
}

func TestFallbackAlternativesFallBackToAPIOnlyWhenPaidFallbackAllowed(t *testing.T) {
	coverage := &fakeCoverage{}
	creds := &fakeFallbackCredentials{}
	in := dispatch.FailoverInputs{
		InitialDecision:  router.Decision{Model: "model-a", Provider: "provider-a"},
		Alternatives:     []router.Decision{{Model: "model-b", Provider: "provider-a"}},
		BuildAlternative: func(router.Decision) (dispatch.UpstreamAttempt, error) { return nil, nil },
	}
	_, err := fallbackWith(coverage, creds).Dispatch(context.Background(), in)

	require.ErrorIs(t, err, dispatch.ErrDispatchWithoutPurpose)
	assert.True(t, coverage.apiOnlyEntered)
	assert.Zero(t, creds.cleared)
	assert.Equal(t, []string{"provider-a", "provider-a", "provider-a"}, creds.resolved)
}
