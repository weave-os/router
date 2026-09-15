package llmescalation

import (
	"context"
	"errors"

	"weave-os/router/internal/flags"
)

// ErrConfigurationConflict indicates a stale operator configuration snapshot.
var ErrConfigurationConflict = errors.New("escalation configuration changed; refresh before saving")

// ErrInstallationNotFound identifies a missing or deleted installation.
var ErrInstallationNotFound = errors.New("escalation installation not found")

// Selection is the installation's active and shadow classifier configuration.
type Selection struct {
	InstallationID    string                     `json:"installation_id"`
	Active            flags.EscalationClassifier `json:"active"`
	Shadow            flags.EscalationClassifier `json:"shadow"`
	Cadence           int                        `json:"cadence"`
	Epoch             int                        `json:"epoch"`
	Ready             bool                       `json:"ready"`
	UnavailableReason string                     `json:"unavailable_reason,omitempty"`
}

// SelectionUpdate uses an expected epoch to prevent lost operator edits.
type SelectionUpdate struct {
	Active  flags.EscalationClassifier `json:"active"`
	Shadow  flags.EscalationClassifier `json:"shadow"`
	Cadence int                        `json:"cadence"`
	Epoch   int                        `json:"epoch"`
	Reset   bool                       `json:"reset"`
}

// ConfigurationStore merges classifier settings without replacing unrelated flags.
type ConfigurationStore interface {
	GetSelection(context.Context, string) (Selection, error)
	SetSelection(context.Context, string, SelectionUpdate) (Selection, error)
}
