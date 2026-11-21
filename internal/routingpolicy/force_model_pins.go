package routingpolicy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const ForceModelSessionRole = "force_model"

type ForceModelPins struct {
	store sessionpin.Store
	now   func() time.Time
}

func NewForceModelPins(store sessionpin.Store, now func() time.Time) ForceModelPins {
	return ForceModelPins{store: store, now: now}
}

func (fp ForceModelPins) preserveControlHistory(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	nextModel string,
) error {
	existing, found, err := fp.store.Get(ctx, sessionKey, ForceModelSessionRole)
	if err != nil {
		return err
	}
	if !found || !IsUserForcedReason(existing.Reason) || existing.Model == "" || existing.Model == nextModel {
		return nil
	}
	return fp.store.UpdateUsage(context.WithoutCancel(ctx), sessionKey, ForceModelSessionRole, sessionpin.Usage{
		Strategy:            existing.Strategy,
		EndedAt:             fp.now(),
		ServedModel:         existing.Model,
		ServedProvider:      existing.Provider,
		PriorServedModel:    existing.LastServedModel,
		SessionEverSwitched: existing.HasEverSwitched,
	})
}

func (fp ForceModelPins) Set(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	installationID uuid.UUID,
	canonicalModel, provider, effort, forcedThreadKey string,
) error {
	if fp.store == nil || installationID == uuid.Nil {
		return nil
	}
	if err := fp.preserveControlHistory(ctx, sessionKey, canonicalModel); err != nil {
		return fmt.Errorf("preserve force-model control history: %w", err)
	}
	forced := sessionpin.Pin{
		SessionKey:      sessionKey,
		Role:            ForceModelSessionRole,
		InstallationID:  installationID,
		Provider:        provider,
		Model:           canonicalModel,
		Effort:          effort,
		ForcedThreadKey: forcedThreadKey,
		Reason:          translate.ReasonUserForceModel,
		TurnCount:       1,
		PinnedUntil:     PinNeverExpires,
	}
	return fp.store.Upsert(context.WithoutCancel(ctx), forced)
}

func (fp ForceModelPins) Load(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	threadKeyHex string,
	isSubagent bool,
) (sessionpin.Pin, bool, bool) {
	if fp.store == nil {
		return sessionpin.Pin{}, false, false
	}
	pin, found, err := fp.store.Get(ctx, sessionKey, ForceModelSessionRole)
	if err != nil {
		observability.FromContext(ctx).Error("force-model session pin lookup failed", "err", err)
		return sessionpin.Pin{}, false, false
	}
	if found && IsUserUnforcedPin(pin) {
		return pin, false, true
	}
	if !found || !pin.PinnedUntil.After(fp.now()) || !IsUserForcedReason(pin.Reason) || pin.Model == "" || pin.Provider == "" {
		return sessionpin.Pin{}, false, false
	}
	if pin.ForcedThreadKey != "" && (isSubagent || pin.ForcedThreadKey != threadKeyHex) {
		return pin, false, true
	}
	return pin, true, false
}

func (fp ForceModelPins) Clear(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) error {
	if fp.store == nil || installationID == uuid.Nil {
		return nil
	}
	if err := fp.preserveControlHistory(ctx, sessionKey, ""); err != nil {
		return fmt.Errorf("preserve force-model control history before clear: %w", err)
	}
	cleared := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           ForceModelSessionRole,
		InstallationID: installationID,
		Reason:         ReasonUserUnforced,
		TurnCount:      1,
		PinnedUntil:    PinNeverExpires,
	}
	return fp.store.Upsert(context.WithoutCancel(ctx), cleared)
}
