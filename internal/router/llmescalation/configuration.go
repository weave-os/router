package llmescalation

import (
	"context"
	"errors"

	"weave-os/router/internal/flags"
)

var ErrConfigurationConflict = errors.New("escalation configuration changed; refresh before saving")

var ErrInstallationNotFound = errors.New("escalation installation not found")

type Selection struct {
	InstallationID    string                     `json:"installation_id"`
	Active            flags.EscalationClassifier `json:"active"`
	Shadow            flags.EscalationClassifier `json:"shadow"`
	Cadence           int                        `json:"cadence"`
	Epoch             int                        `json:"epoch"`
	Ready             bool                       `json:"ready"`
	UnavailableReason string                     `json:"unavailable_reason,omitempty"`
}

type SelectionUpdate struct {
	Active  flags.EscalationClassifier `json:"active"`
	Shadow  flags.EscalationClassifier `json:"shadow"`
	Cadence int                        `json:"cadence"`
	Epoch   int                        `json:"epoch"`
	Reset   bool                       `json:"reset"`
}

type ConfigurationStore interface {
	GetSelection(context.Context, string) (Selection, error)
	SetSelection(context.Context, string, SelectionUpdate) (Selection, error)
}
