package conversation

import "weave-os/router/internal/router/sessionpin"

const commandContinuationRoleSuffix = "_cmd_next"

func CommandContinuationRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + commandContinuationRoleSuffix
}
