package policy

import (
	"context"
	"errors"
	"regexp"
	"weave-os/router/internal/router"
)

type PreparationState string

const (
	PreparationWarming PreparationState = "warming"
	PreparationReady   PreparationState = "ready"
)

var ErrClassifierPreparationUnavailable = errors.New("classifier preparation unavailable")

type ClassifierPreparation struct {
	ModelID         string           `json:"model_id"`
	BundleSHA256    string           `json:"bundle_sha256"`
	ServingRevision string           `json:"serving_revision"`
	State           PreparationState `json:"state"`
	OperationID     string           `json:"operation_id"`
	WarmupLengths   []int            `json:"warmup_lengths"`
}

type ClassifierPreparer interface {
	PrepareClassifier(context.Context, string) (ClassifierPreparation, error)
}

var preparationOperationPattern = regexp.MustCompile(`^fc-[A-Za-z0-9_-]{1,125}$`)

func ValidPreparationOperationID(operationID string) bool {
	return preparationOperationPattern.MatchString(operationID)
}

func (r *SidecarRouter) PrepareClassifier(ctx context.Context, operationID string) (ClassifierPreparation, error) {
	preparer, ok := r.decider.(ClassifierPreparer)
	if !ok {
		return ClassifierPreparation{}, ErrClassifierPreparationUnavailable
	}
	return preparer.PrepareClassifier(ctx, operationID)
}

var (
	ErrModelPreparationForbidden = errors.New("model preparation not permitted")
	ErrModelPreparationOperation = errors.New("model preparation operation does not match admitted release")
)

type ModelPreparation struct {
	ClassifierPreparation
	Model     router.RoutingModel `json:"model"`
	ReleaseID string              `json:"release_id"`
}
