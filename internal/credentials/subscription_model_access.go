package credentials

import (
	"errors"
	"net/http"
	"strings"
	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
)

func AnthropicSubscriptionModelRejected(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusNotFound {
		return false
	}
	return gjson.GetBytes(upstream.Body, "error.type").String() == "not_found_error" &&
		strings.HasPrefix(strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String()), "model:")
}
