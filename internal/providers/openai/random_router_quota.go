package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"weave-os/router/internal/requestcontext"
)

func decodeCodexQuota(ctx context.Context, body []byte, target any) error {
	if requestcontext.IncludedSubscriptionOnlyFrom(ctx) {
		var metadata struct {
			Credits *struct {
				HasCredits *bool `json:"has_credits"`
				Unlimited  bool  `json:"unlimited"`
			} `json:"credits"`
			RateLimit *struct {
				Primary   json.RawMessage `json:"primary_window"`
				Secondary json.RawMessage `json:"secondary_window"`
			} `json:"rate_limit"`
		}
		if err := json.Unmarshal(body, &metadata); err != nil {
			return err
		}
		if metadata.Credits == nil || metadata.Credits.HasCredits == nil || *metadata.Credits.HasCredits || metadata.Credits.Unlimited || metadata.RateLimit == nil || (!codexWindowPresent(metadata.RateLimit.Primary) && !codexWindowPresent(metadata.RateLimit.Secondary)) {
			return errors.New("included subscription capacity cannot be verified")
		}
	}
	return json.Unmarshal(body, target)
}

func codexWindowPresent(window json.RawMessage) bool {
	value := bytes.TrimSpace(window)
	return len(value) > 0 && !bytes.Equal(value, []byte("null"))
}

func codexQuotaCreditAdmission(ctx context.Context, body []byte, deployment bool) (string, error) {
	if requestcontext.IncludedSubscriptionOnlyFrom(ctx) || (!deployment && !requestcontext.SubscriptionExtraUsageFrom(ctx)) {
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
