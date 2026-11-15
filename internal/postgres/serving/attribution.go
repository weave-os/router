package serving

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

type RequestAttributionRepo struct{ queries *sqlc.Queries }

func NewRequestAttributionRepo(pool *pgxpool.Pool) *RequestAttributionRepo {
	return &RequestAttributionRepo{queries: dbbudget.Queries(pool)}
}

func (r *RequestAttributionRepo) RecordServingRequest(ctx context.Context, requestID string, assertion policyregistry.ServingAssertion) error {
	installation, err := uuid.Parse(assertion.Scope.InstallationID)
	if err != nil {
		return err
	}
	key, err := uuid.Parse(assertion.APIKeyID)
	if err != nil {
		return err
	}
	scope, err := json.Marshal(assertion.Scope)
	if err != nil {
		return err
	}
	binding, err := json.Marshal(assertion.Admission)
	if err != nil {
		return err
	}
	return r.queries.InsertServingRequestAttribution(ctx, sqlc.InsertServingRequestAttributionParams{RequestID: requestID, InstallationID: installation, APIKeyID: key, Scope: scope, Binding: binding})
}
