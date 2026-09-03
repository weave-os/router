package postgres

import (
	"time"

	"workweave/router/internal/auth"
	"workweave/router/internal/flags"
	"workweave/router/internal/observability"
	"workweave/router/internal/router"
	"workweave/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func toAuthInstallation(row sqlc.RouterModelRouterInstallation) *auth.Installation {
	excluded := row.ExcludedModels
	if excluded == nil {
		excluded = []string{}
	}
	excludedProviders := row.ExcludedProviders
	if excludedProviders == nil {
		excludedProviders = []string{}
	}
	preferred := row.PreferredModels
	if preferred == nil {
		preferred = []string{}
	}
	allowed := row.AllowedModels
	if allowed == nil {
		allowed = []string{}
	}
	modelsWhenSubscriptionActive := row.ModelsWhenSubscriptionActive
	if modelsWhenSubscriptionActive == nil {
		modelsWhenSubscriptionActive = []string{}
	}
	modelsWhenSubscriptionInactive := row.ModelsWhenSubscriptionInactive
	if modelsWhenSubscriptionInactive == nil {
		modelsWhenSubscriptionInactive = []string{}
	}
	fastModeModels := row.FastModeModels
	if fastModeModels == nil {
		fastModeModels = []string{}
	}
	// Parsed once per API-key cache fill, not per request. A malformed or
	// retired-key payload degrades to "no overrides" rather than failing the
	// request — a stored row must not be able to take an org's traffic down.
	overrides, err := flags.ParseOverrides(row.FlagOverrides)
	if err != nil {
		observability.Get().Error(
			"Failed to parse installation flag overrides; falling back to deployment defaults",
			"err", err,
			"installation_id", row.ID.String(),
			"external_id", row.ExternalID,
		)
		overrides = flags.Overrides{}
	}
	return &auth.Installation{
		ID:                             row.ID.String(),
		ExternalID:                     row.ExternalID,
		Name:                           row.Name,
		CreatedAt:                      timestampOrZero(row.CreatedAt),
		UpdatedAt:                      timestampOrZero(row.UpdatedAt),
		DeletedAt:                      timestampPtr(row.DeletedAt),
		CreatedBy:                      row.CreatedBy,
		ExcludedModels:                 excluded,
		AllowedModels:                  allowed,
		ModelsWhenSubscriptionActive:   modelsWhenSubscriptionActive,
		ModelsWhenSubscriptionInactive: modelsWhenSubscriptionInactive,
		ExcludedProviders:              excludedProviders,
		PreferredModels:                preferred,
		FastModeModels:                 fastModeModels,
		RoutingQualityWeight:           row.RoutingQualityWeight,
		UsageBypassEnabled:             row.UsageBypassEnabled,
		UsageBypassThreshold:           row.UsageBypassThreshold,
		SubscriptionRoutingDisabled:    row.SubscriptionRoutingDisabled,
		RoutingStrategy:                router.Strategy(derefString(row.RoutingStrategy)),
		RoutingRolloutID:               derefString(row.RoutingRolloutID),
		PolicyShadowStrategy:           router.Strategy(derefString(row.PolicyShadowStrategy)),
		PolicyDebugEnabled:             row.PolicyDebugEnabled,
		PolicyHeaderOverridesEnabled:   row.PolicyHeaderOverridesEnabled,
		PolicyRoutingIntent:            derefString(row.PolicyRoutingIntent),
		AITrainingAllowed:              row.AiTrainingAllowed,
		ByokEnabled:                    row.ByokEnabled,
		ContentCaptureMode:             row.ContentCaptureMode,
		HideTerminalSurfaces:           row.HideTerminalSurfaces,
		FirstRequestServedAt:           timestamptzPtr(row.FirstRequestServedAt),
		FlagOverrides:                  overrides,
	}
}

func toAuthAPIKey(row sqlc.RouterModelRouterAPIKey) *auth.APIKey {
	return &auth.APIKey{
		ID:             row.ID.String(),
		InstallationID: row.InstallationID.String(),
		ExternalID:     row.ExternalID,
		Name:           row.Name,
		KeyPrefix:      row.KeyPrefix,
		KeyHash:        row.KeyHash,
		KeySuffix:      row.KeySuffix,
		Scope:          auth.APIKeyScope(row.Scope),
		LastUsedAt:     timestampPtr(row.LastUsedAt),
		CreatedAt:      timestampOrZero(row.CreatedAt),
		DeletedAt:      timestampPtr(row.DeletedAt),
		CreatedBy:      row.CreatedBy,
	}
}

func timestampOrZero(t pgtype.Timestamp) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func timestampPtr(t pgtype.Timestamp) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

func timestamptzPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

// stringPtrOrNil returns nil for empty strings so SQLC's nullable columns receive NULL instead of "".
func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uuidOrNil parses s into a pgtype.UUID, returning an invalid (NULL) value for
// empty or malformed input so SQLC's nullable uuid column receives NULL.
func uuidOrNil(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

// uuidString returns the canonical string form of a pgtype.UUID, or "" when NULL.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}
