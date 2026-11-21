package middleware_test

import (
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"

	"github.com/stretchr/testify/assert"
)

func TestBillingMiddlewareRefusesToBuildWithoutCustomerFunding(t *testing.T) {
	svc := billing.NewService(nil)
	for name, build := range map[string]func(){
		"balance_check":         func() { middleware.WithBalanceCheck(svc, 0, nil) },
		"api_key_spend_cap":     func() { middleware.WithAPIKeySpendCap(svc, nil) },
		"delegated_spend_cap":   func() { middleware.WithDelegatedSpendCap(svc, nil) },
		"org_monthly_spend_cap": func() { middleware.WithOrgMonthlySpendCap(svc, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, build, "an unwired funding port must fail at startup, not on the first request")
		})
	}
}
