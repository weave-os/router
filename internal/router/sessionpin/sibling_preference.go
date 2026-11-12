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

// SiblingPreferenceStore persists conversation-scoped model order independent
// of expiring thread pins. Successful writes reject older request starts.
type SiblingPreferenceStore interface {
	GetSiblingPreferences(context.Context, uuid.UUID, [SessionKeyLen]byte, router.Strategy) (map[escalation.Group]router.SiblingPreference, error)
	RecordSiblingPreference(context.Context, uuid.UUID, [SessionKeyLen]byte, router.Strategy, escalation.Group, string, []string, time.Time) (bool, error)
	ClearSiblingWinnerExclusions(context.Context, [SessionKeyLen]byte, router.Strategy, []string, string, time.Time) error
}
