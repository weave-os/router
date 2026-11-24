package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

var CodexQuotaErrorTypes = map[string]struct{}{
	"usage_limit_reached": {},
	"insufficient_quota":  {},
}

const CodexQuotaWindowMinutes = 5 * 60

func CodexExhaustionWindow(resetAt time.Time, now time.Time) usage.Window {
	minutes := CodexQuotaWindowMinutes
	if !resetAt.IsZero() {
		if untilReset := int(resetAt.Sub(now).Minutes()) + 1; untilReset > minutes {
			minutes = untilReset
		}
	}
	return usage.Window{UsedPercent: 1, WindowMinutes: minutes, ResetAt: resetAt}
}

func CodexSubscriptionModelRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || (buffered.Status != http.StatusBadRequest && buffered.Status != http.StatusNotFound) {
		return false
	}
	var errorEnvelope struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &errorEnvelope); jsonErr != nil {
		return false
	}
	switch errorEnvelope.Error.Code {
	case "model_not_found", "unsupported_model", "model_not_available":
		return true
	}
	return errorEnvelope.Error.Param == "model" && errorEnvelope.Error.Type == "invalid_request_error"
}

func CodexSubscriptionModelUnavailable(model string) error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    "invalid_request_error",
			"code":    "model_not_found",
			"param":   "model",
			"message": "The model " + router.StripDateSuffix(model) + " is unavailable to this subscription.",
		},
	})
	return &providers.UpstreamErrorResponse{Status: http.StatusNotFound, Body: body}
}

func CodexOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return buffered.Status == http.StatusUnauthorized || buffered.Status == http.StatusForbidden
}

func CodexQuotaExhaustion(err error) (time.Time, bool) {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return time.Time{}, false
	}
	var env struct {
		Error struct {
			Type     string `json:"type"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &env); jsonErr != nil {
		return time.Time{}, false
	}
	if _, ok := CodexQuotaErrorTypes[env.Error.Type]; !ok {
		return time.Time{}, false
	}
	if env.Error.ResetsAt <= 0 {
		return time.Time{}, true
	}
	return time.Unix(env.Error.ResetsAt, 0).UTC(), true
}

func CodexSubscriptionRotates(credentialCtx context.Context, err error) bool {
	if !ServedOnCodexSubscription(credentialCtx) {
		return false
	}
	_, quotaSpent := CodexQuotaExhaustion(err)
	return quotaSpent || CodexOAuthCredentialRejected(err) || CodexSubscriptionModelRejected(err) || providers.IsRetryable(err)
}
