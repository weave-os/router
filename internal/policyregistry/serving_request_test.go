package policyregistry_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/require"
)

type admittedTestRouter struct {
	model        string
	capabilities policy.Capabilities
}

func (r admittedTestRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Model: r.model}, nil
}
func (r admittedTestRouter) CurrentCapabilities() policy.Capabilities { return r.capabilities }

type snapshotReporter struct {
	admittedTestRouter
	reports chan string
}

func (r snapshotReporter) ReportOutcome(context.Context, map[string]interface{}) error {
	r.reports <- r.model + ":outcome"
	return nil
}

func (r snapshotReporter) ReportFeedback(context.Context, map[string]interface{}) error {
	r.reports <- r.model + ":feedback"
	return nil
}

func TestDetachedOutcomeKeepsAdmittedSnapshotAcrossRebind(t *testing.T) {
	reports := make(chan string, 3)
	makeSnapshot := func(model string) *policyregistry.Snapshot {
		return &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMM: snapshotReporter{admittedTestRouter: admittedTestRouter{model: model}, reports: reports}}}
	}
	runtime := policyregistry.NewAdmittedRouter(router.StrategyHMM, makeSnapshot("bootstrap"))
	old, cancel := context.WithCancel(policyregistry.WithServingSnapshot(context.Background(), makeSnapshot("retained-profile")))
	resume := make(chan struct{})
	finished := make(chan error, 1)
	observability.SafeGoContext(old, slog.Default(), time.Second, "retained-outcome", func(ctx context.Context) {
		<-resume
		finished <- runtime.ReportOutcome(ctx, nil)
	})
	cancel()
	current := policyregistry.WithServingSnapshot(context.Background(), makeSnapshot("rebound-profile"))
	require.NoError(t, runtime.ReportFeedback(current, nil))
	close(resume)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("retained outcome did not finish")
	}
	require.Equal(t, "rebound-profile:feedback", <-reports)
	require.Equal(t, "retained-profile:outcome", <-reports)
	require.ErrorIs(t, runtime.ReportFeedback(context.Background(), nil), policyregistry.ErrNoActivePolicy)
}

func TestAdmittedRouterNeverFallsBackToPreparedBaseline(t *testing.T) {
	baseline := &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMM: admittedTestRouter{model: "baseline"}}}
	selected := &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMM: admittedTestRouter{model: "profile", capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}}}}
	runtime := policyregistry.NewAdmittedRouter(router.StrategyHMM, baseline)
	_, err := runtime.Route(context.Background(), router.Request{})
	require.ErrorIs(t, err, policyregistry.ErrNoActivePolicy)
	ctx := policyregistry.WithServingSnapshot(context.Background(), selected)
	decision, err := runtime.Route(ctx, router.Request{})
	require.NoError(t, err)
	require.Equal(t, "profile", decision.Model)
	require.True(t, runtime.CapabilitiesForRequest(ctx).AuthoritativePerTurnSelection)
	require.False(t, runtime.CurrentCapabilities().AuthoritativePerTurnSelection)
}

func TestAdmittedIdentitySeparatesIncarnationsAndSessionlessRequests(t *testing.T) {
	assertion := policyregistry.ServingAssertion{
		Scope:     policyregistry.AdmissionScope{InstallationID: "installation", CredentialIdentity: "subject", Persistent: true, ConversationDigest: [16]byte{1}},
		Admission: policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "a", BindingGeneration: 1},
	}
	identity := func(a policyregistry.ServingAssertion) requestcontext.ServingIdentity {
		ctx := policyregistry.WithServingAssertion(context.Background(), a)
		value, ok := requestcontext.ServingIdentityFromContext(ctx)
		require.True(t, ok)
		return value
	}
	first := identity(assertion)
	require.Equal(t, first.StateNamespace, identity(assertion).StateNamespace)
	assertion.Admission.BindingGeneration++
	require.NotEqual(t, first.StateNamespace, identity(assertion).StateNamespace)
	assertion.Scope.Persistent = false
	unscoped := identity(assertion).StateNamespace
	require.NotEqual(t, unscoped, identity(assertion).StateNamespace, "missing session ID must not share learned state")
}

func TestAdmittedCredentialIdentityKeepsSharedKeyBytesAndScopesPersonalSubjects(t *testing.T) {
	identity := func(apiKeyID, installation, subject string) string {
		ctx := policyregistry.WithServingAssertion(context.Background(), policyregistry.ServingAssertion{
			APIKeyID: apiKeyID,
			Scope:    policyregistry.AdmissionScope{InstallationID: installation, CredentialIdentity: subject, Persistent: true},
		})
		value, ok := requestcontext.ServingIdentityFromContext(ctx)
		require.True(t, ok)
		return value.CredentialIdentity
	}
	require.Equal(t, "shared-key", identity("shared-key", "installation-a", "shared-key"), "shared credentials keep legacy preference key bytes")
	personal := identity("key-1", "installation-a", "subject")
	require.Equal(t, personal, identity("key-2", "installation-a", "subject"), "personal subjects survive key rotation")
	require.NotEqual(t, personal, identity("key-1", "installation-b", "subject"), "personal subjects must not share preferences across installations")
	require.NotEqual(t, "subject", personal)
}
