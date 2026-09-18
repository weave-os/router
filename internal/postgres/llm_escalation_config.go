package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/sqlc"
)

var _ llmescalation.ConfigurationStore = (*LLMEscalationRepo)(nil)

// GetSelection resolves the installation's explicit or legacy classifier settings.
func (r *LLMEscalationRepo) GetSelection(ctx context.Context, installation string) (llmescalation.Selection, error) {
	id, err := uuid.Parse(installation)
	if err != nil {
		return llmescalation.Selection{}, fmt.Errorf("parse escalation installation: %w", err)
	}
	encoded, err := sqlc.New(r.pool).GetEscalationSelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return llmescalation.Selection{}, llmescalation.ErrInstallationNotFound
	}
	if err != nil {
		return llmescalation.Selection{}, err
	}
	return escalationSelection(installation, encoded)
}

// SetSelection atomically patches only escalation settings and their legacy mirrors.
func (r *LLMEscalationRepo) SetSelection(ctx context.Context, installation string, update llmescalation.SelectionUpdate) (llmescalation.Selection, error) {
	id, err := uuid.Parse(installation)
	if err != nil {
		return llmescalation.Selection{}, fmt.Errorf("parse escalation installation: %w", err)
	}
	if update.Epoch < 0 || update.Epoch >= math.MaxInt32 {
		return llmescalation.Selection{}, fmt.Errorf("%w: escalation epoch exceeds its supported range", flags.ErrInvalidValue)
	}
	current, err := r.GetSelection(ctx, installation)
	if err != nil {
		return llmescalation.Selection{}, err
	}
	if current.Epoch != update.Epoch {
		return llmescalation.Selection{}, llmescalation.ErrConfigurationConflict
	}
	nextEpoch := update.Epoch
	if update.Reset || current.Active != update.Active || current.Shadow != update.Shadow || current.Cadence != update.Cadence {
		nextEpoch++
	}
	patch := flags.Overrides{
		Strings: map[flags.Key]string{flags.KeyEscalationActiveClassifier: string(update.Active), flags.KeyEscalationShadowClassifier: string(update.Shadow)},
		Ints:    map[flags.Key]int{flags.KeyEscalationCadence: update.Cadence, flags.KeyEscalationEpoch: nextEpoch, flags.KeyEscalationXGBoostEpoch: nextEpoch},
		Bools:   map[flags.Key]bool{flags.KeyEscalationXGBoostEnabled: update.Active == flags.EscalationClassifierXGB, flags.KeyEscalationXGBoostShadowEnabled: update.Shadow == flags.EscalationClassifierXGB},
	}
	if err := flags.ValidateOverrides(patch); err != nil {
		return llmescalation.Selection{}, err
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return llmescalation.Selection{}, err
	}
	updated, err := sqlc.New(r.pool).UpdateEscalationSelection(ctx, sqlc.UpdateEscalationSelectionParams{InstallationID: id, SelectionPatch: encoded, ExpectedEpoch: int32(update.Epoch)})
	if errors.Is(err, sql.ErrNoRows) {
		_, lookupErr := r.GetSelection(ctx, installation)
		if lookupErr != nil {
			return llmescalation.Selection{}, lookupErr
		}
		return llmescalation.Selection{}, llmescalation.ErrConfigurationConflict
	}
	if err != nil {
		return llmescalation.Selection{}, err
	}
	return escalationSelection(installation, updated)
}

func escalationSelection(installation string, encoded []byte) (llmescalation.Selection, error) {
	overrides, err := flags.ParseOverrides(encoded)
	if err != nil {
		return llmescalation.Selection{}, err
	}
	effective := flags.EscalationFromContext(flags.WithOverrides(context.Background(), overrides))
	return llmescalation.Selection{InstallationID: installation, Active: effective.Active, Shadow: effective.Shadow, Cadence: effective.Cadence, Epoch: effective.Epoch}, nil
}
