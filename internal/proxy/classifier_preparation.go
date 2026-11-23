package proxy

import (
	"context"
	"fmt"
	"strings"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func (s *Service) PrepareRouterModel(ctx context.Context, model router.RoutingModel, operation string) (policy.ModelPreparation, error) {
	identity, _ := requestcontext.ServingIdentityFromContext(ctx)
	if !internalBetaSession(ctx) || model != router.ModelQwen {
		return policy.ModelPreparation{}, policy.ErrModelPreparationForbidden
	}
	snapshot := policyregistry.ServingSnapshotFromContext(ctx)
	if snapshot == nil || snapshot.HeadSnapshot.Head.ReleaseSHA256 != identity.ReleaseID {
		return policy.ModelPreparation{}, policy.ErrClassifierPreparationUnavailable
	}
	artifact, found := snapshot.Release.Classifier.Variants[router.StrategyHMMQwen]
	preparer, available := snapshot.Routers[router.StrategyHMMQwen].(policy.ClassifierPreparer)
	if !found || !available || artifact.ServingRevision == "" {
		return policy.ModelPreparation{}, policy.ErrClassifierPreparationUnavailable
	}
	prefix := identity.ReleaseID + "." + string(router.StrategyHMMQwen) + "."
	backendOperation := ""
	if operation != "" {
		if len(operation) > 256 || !strings.HasPrefix(operation, prefix) {
			return policy.ModelPreparation{}, policy.ErrModelPreparationOperation
		}
		backendOperation = strings.TrimPrefix(operation, prefix)
		if !policy.ValidPreparationOperationID(backendOperation) {
			return policy.ModelPreparation{}, policy.ErrModelPreparationOperation
		}
	}
	preparation, err := preparer.PrepareClassifier(ctx, backendOperation)
	if err != nil {
		return policy.ModelPreparation{}, fmt.Errorf("prepare admitted classifier: %w", err)
	}
	if preparation.ModelID != artifact.ArtifactID || preparation.BundleSHA256 != artifact.PackageSHA256 || preparation.ServingRevision != artifact.ServingRevision ||
		!policy.ValidPreparationOperationID(preparation.OperationID) || backendOperation != "" && preparation.OperationID != backendOperation ||
		preparation.State != policy.PreparationReady && preparation.State != policy.PreparationWarming {
		return policy.ModelPreparation{}, fmt.Errorf("classifier preparation identity does not match admitted release")
	}
	preparation.OperationID = prefix + preparation.OperationID
	return policy.ModelPreparation{ClassifierPreparation: preparation, Model: model, ReleaseID: identity.ReleaseID}, nil
}
