package sessionpin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

// SiblingPinSuperseded reports whether a remembered sibling preference makes a
// router-chosen pin stale: the pin's group remembers a different winner.
func SiblingPinSuperseded(pin Pin, req router.Request) bool {
	preference, remembered := req.SiblingPreferences[escalation.Group(pin.PolicyGroup)]
	return remembered && preference.Model != "" && pin.Model != preference.Model
}

// SiblingPreferenceWrite is one "the sibling that rescued the group now leads
// it" record for a conversation digest.
type SiblingPreferenceWrite struct {
	InstallationID uuid.UUID
	SessionKey     [SessionKeyLen]byte
	Strategy       router.Strategy
	Group          escalation.Group
	Preferred      string
	Displaced      []string
	StartedAt      time.Time
}

// SiblingPreferenceStore persists conversation-scoped model order independent
// of expiring thread pins. Successful writes reject older request starts.
type SiblingPreferenceStore interface {
	GetSiblingPreferences(context.Context, uuid.UUID, [SessionKeyLen]byte, router.Strategy) (map[escalation.Group]router.SiblingPreference, error)
	RecordSiblingPreference(context.Context, SiblingPreferenceWrite) (bool, error)
	ClearSiblingWinnerExclusions(context.Context, [SessionKeyLen]byte, router.Strategy, []string, string, time.Time) error
}
