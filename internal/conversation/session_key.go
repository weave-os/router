package conversation

import (
	"fmt"

	"weave-os/router/internal/router/sessionpin"
)

func ShortSessionKey(sessionKey [sessionpin.SessionKeyLen]byte) string {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ""
	}
	return fmt.Sprintf("%x", sessionKey[:8])
}
