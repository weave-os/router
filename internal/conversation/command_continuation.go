package conversation

import "weave-os/router/internal/router/sessionpin"

// Roles live in router.session_pins.role (VARCHAR(32)); the longest normal
// role is default_high, so keep this internal suffix compact.
const commandContinuationRoleSuffix = "_cmd_next"

func CommandContinuationRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + commandContinuationRoleSuffix
}
