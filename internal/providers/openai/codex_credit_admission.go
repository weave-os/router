package openai

import (
	"context"
	"encoding/json"
	"weave-os/router/internal/requestcontext"
)

func codexQuotaCreditAdmission(ctx context.Context, body []byte, deployment bool) (string, error) {
	if !deployment && !requestcontext.SubscriptionExtraUsageFrom(ctx) {
		return codexCreditsDisabled, nil
	}
	var metadata struct {
		Credits      json.RawMessage `json:"credits"`
		SpendControl json.RawMessage `json:"spend_control"`
		ReachedType  json.RawMessage `json:"rate_limit_reached_type"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return codexCreditsDisabled, err
	}
	return codexCreditAdmissionReason(metadata.Credits, metadata.SpendControl, metadata.ReachedType), nil
}
