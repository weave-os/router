package sessionpin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

// SiblingPreferenceStore persists conversation-scoped model order independent
// of expiring thread pins. Successful writes reject older request starts.
type SiblingPreferenceStore interface {
	GetSiblingPreferences(context.Context, uuid.UUID, [SessionKeyLen]byte, router.Strategy) (map[escalation.Group]router.SiblingPreference, error)
	RecordSiblingPreference(context.Context, uuid.UUID, [SessionKeyLen]byte, router.Strategy, escalation.Group, string, []string, time.Time) (bool, error)
	ClearSiblingWinnerExclusions(context.Context, [SessionKeyLen]byte, router.Strategy, []string, string, time.Time) error
}
