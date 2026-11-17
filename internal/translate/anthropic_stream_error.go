package translate

import (
	"fmt"
	"net/http"

	"github.com/tidwall/gjson"
)

func anthropicErrorTypeForStatus(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusServiceUnavailable:
		return "overloaded_error"
	case status >= 500:
		return "api_error"
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

func upstreamErrorMessage(body string, status int) string {
	if body != "" {
		if m := gjson.Get(body, "error.message"); m.Exists() && m.String() != "" {
			return m.String()
		}
		if m := gjson.Get(body, "message"); m.Exists() && m.String() != "" {
			return m.String()
		}
	}
	return fmt.Sprintf("upstream provider returned HTTP %d", status)
}
