package openai

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"weave-os/router/internal/providers"
)

const (
	codexCreditsDisabled  = "disabled"
	codexCreditsAvailable = "available"
)

func codexIncludedLimitReached(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	return errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests
}

// Credit metadata is read only after the operator opts in. Missing or invalid
// quota data and provider spend controls never become permission to spend.
func codexCreditAdmissionReason(raw, spend, reached json.RawMessage) string {
	var credits struct {
		HasCredits *bool   `json:"has_credits"`
		Unlimited  bool    `json:"unlimited"`
		Balance    *string `json:"balance"`
	}
	if json.Unmarshal(raw, &credits) != nil {
		return "credits_invalid"
	}
	if !credits.Unlimited {
		if credits.HasCredits == nil || !*credits.HasCredits {
			return "credits_not_available"
		}
		// Subscription OAuth responses may explicitly report available credits
		// while hiding the numeric balance.
		if credits.Balance != nil && !positiveCreditBalance(*credits.Balance) {
			return "credits_balance_not_positive"
		}
	}
	if len(reached) > 0 && string(reached) != "null" {
		var reason struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(reached, &reason) != nil || reason.Type != "rate_limit_reached" {
			return "provider_limit_not_included"
		}
	}
	if len(spend) > 0 && string(spend) != "null" {
		var control struct {
			Reached    *bool `json:"reached"`
			Individual *struct {
				Remaining *string `json:"remaining"`
			} `json:"individual_limit"`
		}
		if json.Unmarshal(spend, &control) != nil || control.Reached == nil || *control.Reached {
			return "spend_control_reached_or_unknown"
		}
		if control.Individual != nil && (control.Individual.Remaining == nil || !positiveCreditBalance(*control.Individual.Remaining)) {
			return "individual_spend_limit_reached_or_unknown"
		}
	}
	return codexCreditsAvailable
}

func positiveCreditBalance(raw string) bool {
	balance, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && !math.IsNaN(balance) && !math.IsInf(balance, 0) && balance > 0
}
