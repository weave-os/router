package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type preparationTestRouter struct {
	preparation policy.ClassifierPreparation
	operations  []string
}

func (p *preparationTestRouter) Route(context.Context, router.Request) (router.Decision, error) {
	panic("preparation must not perform inference")
}

func (p *preparationTestRouter) PrepareClassifier(_ context.Context, operation string) (policy.ClassifierPreparation, error) {
	p.operations = append(p.operations, operation)
	return p.preparation, nil
}

func preparationContext() (context.Context, *policyregistry.Snapshot, *preparationTestRouter) {
	preparer := &preparationTestRouter{preparation: policy.ClassifierPreparation{
		ModelID: "qwen-artifact", BundleSHA256: strings.Repeat("b", 64), ServingRevision: strings.Repeat("c", 64),
		State: policy.PreparationWarming, OperationID: "fc-synthetic1",
	}}
	snapshot := &policyregistry.Snapshot{
		Candidate: policyregistry.Candidate{
			HeadSnapshot: policyregistry.HeadSnapshot{Head: policyregistry.LaneHead{ReleaseSHA256: strings.Repeat("a", 64)}},
			Release: policyregistry.Release{Classifier: policyregistry.ClassifierIdentity{Variants: map[router.Strategy]policy.ClassifierArtifact{
				router.StrategyHMMQwen: {ArtifactID: "qwen-artifact", PackageSHA256: strings.Repeat("b", 64), ServingRevision: strings.Repeat("c", 64)},
			}}},
		},
		Routers: map[router.Strategy]router.Router{router.StrategyHMMQwen: preparer},
	}
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{
		Target: string(policyregistry.TargetInternal), ReleaseID: strings.Repeat("a", 64), BindingID: strings.Repeat("d", 64), CredentialIdentity: "synthetic-subject",
	})
	return policyregistry.WithServingSnapshot(ctx, snapshot), snapshot, preparer
}

func TestPrepareRouterModelUsesAdmittedClassifierAndRejectsReleaseChanges(t *testing.T) {
	ctx, snapshot, preparer := preparationContext()
	service := &Service{}
	warming, err := service.PrepareRouterModel(ctx, router.ModelQwen, "")
	require.NoError(t, err)
	require.Equal(t, policy.PreparationWarming, warming.State)
	require.Equal(t, strings.Repeat("a", 64)+".hmm_qwen.fc-synthetic1", warming.OperationID)
	require.Equal(t, router.ModelQwen, warming.Model)

	preparer.preparation.State = policy.PreparationReady
	ready, err := service.PrepareRouterModel(ctx, router.ModelQwen, warming.OperationID)
	require.NoError(t, err)
	require.Equal(t, policy.PreparationReady, ready.State)
	require.Equal(t, []string{"", "fc-synthetic1"}, preparer.operations)

	identity, _ := requestcontext.ServingIdentityFromContext(ctx)
	identity.ReleaseID = strings.Repeat("e", 64)
	snapshot.HeadSnapshot.Head.ReleaseSHA256 = identity.ReleaseID
	ctx = requestcontext.WithServingIdentity(ctx, identity)
	_, err = service.PrepareRouterModel(ctx, router.ModelQwen, warming.OperationID)
	require.ErrorIs(t, err, ErrModelPreparationOperation)
	require.Len(t, preparer.operations, 2)
}

func TestPrepareRouterModelRejectsUnpermittedScopesBeforeSidecar(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(context.Context, *policyregistry.Snapshot) context.Context
	}{
		{"stable", func(ctx context.Context, _ *policyregistry.Snapshot) context.Context {
			identity, _ := requestcontext.ServingIdentityFromContext(ctx)
			identity.Target = string(policyregistry.TargetStable)
			return requestcontext.WithServingIdentity(ctx, identity)
		}},
		{"test plan", func(ctx context.Context, _ *policyregistry.Snapshot) context.Context {
			identity, _ := requestcontext.ServingIdentityFromContext(ctx)
			identity.Plan = string(policyregistry.TestPlanStable)
			return requestcontext.WithServingIdentity(ctx, identity)
		}},
		{"no admission", func(context.Context, *policyregistry.Snapshot) context.Context { return context.Background() }},
		{"snapshot mismatch", func(ctx context.Context, snapshot *policyregistry.Snapshot) context.Context {
			snapshot.HeadSnapshot.Head.ReleaseSHA256 = strings.Repeat("e", 64)
			return ctx
		}},
		{"variant missing", func(ctx context.Context, snapshot *policyregistry.Snapshot) context.Context {
			delete(snapshot.Routers, router.StrategyHMMQwen)
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, snapshot, preparer := preparationContext()
			_, err := (&Service{}).PrepareRouterModel(tc.edit(ctx, snapshot), router.ModelQwen, "")
			require.Error(t, err)
			require.Empty(t, preparer.operations)
		})
	}
}

func TestPrepareRouterModelRejectsUntrustedIdentityAndUnsafeOperation(t *testing.T) {
	for _, field := range []string{"artifact", "bundle", "serving revision", "operation", "state"} {
		t.Run(field, func(t *testing.T) {
			ctx, _, preparer := preparationContext()
			switch field {
			case "artifact":
				preparer.preparation.ModelID = "different-model"
			case "bundle":
				preparer.preparation.BundleSHA256 = strings.Repeat("e", 64)
			case "serving revision":
				preparer.preparation.ServingRevision = strings.Repeat("e", 64)
			case "operation":
				preparer.preparation.OperationID = "../readyz"
			case "state":
				preparer.preparation.State = policy.PreparationState("failed")
			}
			_, err := (&Service{}).PrepareRouterModel(ctx, router.ModelQwen, "")
			require.Error(t, err)
		})
	}
	ctx, _, preparer := preparationContext()
	_, err := (&Service{}).PrepareRouterModel(ctx, router.ModelHMM, "")
	require.ErrorIs(t, err, ErrModelPreparationForbidden)
	_, err = (&Service{}).PrepareRouterModel(ctx, router.ModelQwen, strings.Repeat("a", 64)+".hmm_qwen.fc-../route")
	require.ErrorIs(t, err, ErrModelPreparationOperation)
	require.Empty(t, preparer.operations)
}
