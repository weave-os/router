package router

import "time"

// SiblingPreference remembers a successful replacement within one policy group.
// Displaced models remain eligible as a last resort when the replacement fails.
type SiblingPreference struct {
	Model           string    `json:"model"`
	DisplacedModels []string  `json:"displaced_models"`
	StartedAt       time.Time `json:"started_at"`
}
