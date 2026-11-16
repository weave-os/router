package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"weave-os/router/internal/requestcontext"
)

type codexQuotaModelsKey struct{}

func withCodexQuotaModels(ctx context.Context, models []string) context.Context {
	return context.WithValue(ctx, codexQuotaModelsKey{}, append([]string(nil), models...))
}
func codexQuotaModelSelected(ctx context.Context, candidate, selected string) bool {
	models, _ := ctx.Value(codexQuotaModelsKey{}).([]string)
	if len(models) == 0 {
		return candidate == selected
	}
	return slices.Contains(models, candidate)
}
func codexExtraUsagePermitted(ctx context.Context, deployment bool) bool {
	return !requestcontext.IncludedSubscriptionOnlyFrom(ctx) && (deployment || requestcontext.SubscriptionExtraUsageFrom(ctx))
}
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
