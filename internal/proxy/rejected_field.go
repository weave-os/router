package proxy

import (
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
)

func rejectedFieldUnchanged(err error, inbound, sent []byte) bool {
	path := providers.UpstreamRejectedField(err)
	if path == "" || sent == nil {
		return false
	}
	sentValue, inboundValue := gjson.GetBytes(sent, path), gjson.GetBytes(inbound, path)
	return sentValue.Exists() == inboundValue.Exists() && sentValue.Raw == inboundValue.Raw
}
