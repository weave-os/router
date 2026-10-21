package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

var (
	ErrModelPreparationForbidden = errors.New("model preparation not permitted")
	ErrModelPreparationOperation = errors.New("model preparation operation does not match admitted release")
)

type ModelPreparation struct {
	policy.ClassifierPreparation
	Model     router.RoutingModel `json:"model"`
	ReleaseID string              `json:"release_id"`
}

// PrepareRouterModel uses the admitted immutable runtime, including on later polls.
func (s *Service) PrepareRouterModel(ctx context.Context, model router.RoutingModel, operation string) (ModelPreparation, error) {
	identity, _ := requestcontext.ServingIdentityFromContext(ctx)
	if !internalBetaSession(ctx) || model != router.ModelQwen {
		return ModelPreparation{}, ErrModelPreparationForbidden
	}
	snapshot := policyregistry.ServingSnapshotFromContext(ctx)
	if snapshot == nil || snapshot.HeadSnapshot.Head.ReleaseSHA256 != identity.ReleaseID {
		return ModelPreparation{}, policy.ErrClassifierPreparationUnavailable
	}
	artifact, found := snapshot.Release.Classifier.Variants[router.StrategyHMMQwen]
	preparer, available := snapshot.Routers[router.StrategyHMMQwen].(policy.ClassifierPreparer)
	if !found || !available || artifact.ServingRevision == "" {
		return ModelPreparation{}, policy.ErrClassifierPreparationUnavailable
	}
	prefix := identity.ReleaseID + "." + string(router.StrategyHMMQwen) + "."
	backendOperation := ""
	if operation != "" {
		if len(operation) > 256 || !strings.HasPrefix(operation, prefix) {
			return ModelPreparation{}, ErrModelPreparationOperation
		}
		backendOperation = strings.TrimPrefix(operation, prefix)
		if !policy.ValidPreparationOperationID(backendOperation) {
			return ModelPreparation{}, ErrModelPreparationOperation
		}
	}
	preparation, err := preparer.PrepareClassifier(ctx, backendOperation)
	if err != nil {
		return ModelPreparation{}, fmt.Errorf("prepare admitted classifier: %w", err)
	}
	if preparation.ModelID != artifact.ArtifactID || preparation.BundleSHA256 != artifact.PackageSHA256 || preparation.ServingRevision != artifact.ServingRevision ||
		!policy.ValidPreparationOperationID(preparation.OperationID) || backendOperation != "" && preparation.OperationID != backendOperation ||
		preparation.State != policy.PreparationReady && preparation.State != policy.PreparationWarming {
		return ModelPreparation{}, fmt.Errorf("classifier preparation identity does not match admitted release")
	}
	preparation.OperationID = prefix + preparation.OperationID
	return ModelPreparation{ClassifierPreparation: preparation, Model: model, ReleaseID: identity.ReleaseID}, nil
}
