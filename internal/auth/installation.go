package auth

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
)

type Installation struct {
	ID                               string
	ExternalID                       string
	Name                             string
	CreatedAt                        time.Time
	UpdatedAt                        time.Time
	DeletedAt                        *time.Time
	CreatedBy                        *string
	ExcludedModels                   []string
	AllowedModels                    []string
	SubscriptionStateModelsEnabled   bool
	ModelsWhenSubscriptionActive     []string
	ModelsWhenSubscriptionInactive   []string
	ExcludedProviders                []string
	PreferredModels                  []string
	FastModeModels                   []string
	RoutingQualityWeight             *float64
	UsageBypassEnabled               bool
	UsageBypassThreshold             *float64
	SubscriptionRoutingDisabled      bool
	PickedModelCreditFallbackEnabled bool
	WeaveCreditInferenceEnabled      bool
	RoutingStrategy                  router.Strategy
	RoutingRolloutID                 string
	PolicyShadowStrategy             router.Strategy
	PolicyDebugEnabled               bool
	PolicyHeaderOverridesEnabled     bool
	PolicyRoutingIntent              string
	AITrainingAllowed                bool
	ByokEnabled                      bool
	ContentCaptureMode               *string
	HideTerminalSurfaces             bool
	ShowModelSelectionReasoning      bool
	TrialCaptureEnabled              bool
	TrialEnrollmentID                string
	FirstRequestServedAt             *time.Time
	FlagOverrides                    flags.Overrides
}

type CreateInstallationParams struct {
	ExternalID string
	Name       string
	CreatedBy  *string
}

type InstallationRepository interface {
	Create(ctx context.Context, params CreateInstallationParams) (*Installation, error)
	Get(ctx context.Context, externalID, id string) (*Installation, error)
	ListForExternalID(ctx context.Context, externalID string) ([]*Installation, error)
	SoftDelete(ctx context.Context, externalID, id string) error
	MarkFirstRequestServed(ctx context.Context, id string) error
	UpdateFastModeModels(ctx context.Context, externalID, id string, models []string) error
	UpdateExcludedModels(ctx context.Context, externalID, id string, models []string) error
	UpdateAllowedModels(ctx context.Context, externalID, id string, models []string) error
	UpdateExcludedProviders(ctx context.Context, externalID, id string, providerNames []string) error
	UpdateRoutingPreference(ctx context.Context, externalID, id string, qualityWeight *float64) error
	UpdateUsageBypass(ctx context.Context, externalID, id string, enabled bool, threshold *float64) error
	UpdateSubscriptionRoutingDisabled(ctx context.Context, externalID, id string, disabled bool) error
	UpdateContentCaptureMode(ctx context.Context, externalID, id string, mode *string) error
	UpdateHideTerminalSurfaces(ctx context.Context, externalID, id string, hide bool) error
	UpdateShowModelSelectionReasoning(ctx context.Context, externalID, id string, show bool) error
	UpdateFlagOverrides(ctx context.Context, externalID, id string, overrides flags.Overrides) error
}
