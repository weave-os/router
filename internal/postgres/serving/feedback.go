package serving

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/sqlc"
)

// FeedbackLookup recovers only persisted request attribution, never the current lane head.
type FeedbackLookup struct{ Queries *sqlc.Queries }

// GetFeedbackAdmission preserves a feedback token's original serving tuple after rebinding.
func (r FeedbackLookup) GetFeedbackAdmission(ctx context.Context, installationID, requestID string) (policyregistry.SessionReleaseBinding, error) {
	var admission policyregistry.SessionReleaseBinding
	installation, err := uuid.Parse(installationID)
	if err != nil {
		return admission, err
	}
	row, err := r.Queries.GetServingRequestAttribution(ctx, sqlc.GetServingRequestAttributionParams{InstallationID: installation, RequestID: requestID})
	if err != nil {
		return admission, err
	}
	var scope policyregistry.AdmissionScope
	if err := json.Unmarshal(row.Scope, &scope); err != nil {
		return admission, err
	}
	if scope.InstallationID != installationID || row.RequestID != requestID {
		return admission, errors.New("feedback attribution scope differs from signed token")
	}
	if err := json.Unmarshal(row.Binding, &admission); err != nil {
		return admission, err
	}
	return admission, nil
}
