package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func SubscriptionFundingFailure(err error) bool {
	return IsSubscriptionPoolError(err) || SubscriptionLaneRefused(err) ||
		CodexSubscriptionModelRejected(err) || AnthropicSubscriptionModelRejected(err) ||
		CodexOAuthCredentialRejected(err) || AnthropicOAuthCredentialRejected(err) || AnthropicSubscriptionThirdPartyRefused(err)
}

func StrikeModelUnlessFundingFailure(ctx context.Context, model string, err error) {
	if SubscriptionFundingFailure(err) {
		return
	}
	requestcontext.RecordTurnFailure(ctx, model)
}

func AnthropicOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	if buffered.Status != http.StatusUnauthorized && buffered.Status != http.StatusForbidden {
		return false
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &env); jsonErr != nil {
		return false
	}
	return env.Error.Type == "authentication_error" || env.Error.Type == "permission_error"
}
