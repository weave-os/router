package proxy

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
)

const upstreamFieldRequired = "Field required"

var rejectedFieldMessage = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*): (.+)$`)

func rejectedFieldUnchanged(err error, inbound, sent []byte) bool {
	var buffered *providers.UpstreamErrorResponse
	if sent == nil || !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	match := rejectedFieldMessage.FindStringSubmatch(providers.UpstreamErrorBodyMessage(err))
	if match == nil {
		return false
	}
	path, verdict := match[1], match[2]
	sentValue, inboundValue := gjson.GetBytes(sent, path), gjson.GetBytes(inbound, path)
	if !sentValue.Exists() && !inboundValue.Exists() {
		return verdict == upstreamFieldRequired
	}
	return sentValue.Exists() && inboundValue.Exists() && sentValue.Raw == inboundValue.Raw
}
