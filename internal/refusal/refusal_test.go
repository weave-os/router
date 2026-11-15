package refusal

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEveryKindHasAContract(t *testing.T) {
	for _, kind := range Kinds() {
		contract := kind.Contract()
		assert.GreaterOrEqual(t, contract.Status, http.StatusBadRequest, "kind %d has no status in the refusal table", kind)
		assert.NotEmpty(t, contract.Code, "kind %d has no code in the refusal table", kind)
		assert.False(t, contract.RetryAfter && contract.NoClientRetry, "kind %d both asks for and forbids a retry", kind)
	}
}

// TestContractTable pins the full refusal table; changing a row is a
// behavior change for every site that refuses with that kind.
func TestContractTable(t *testing.T) {
	want := map[Kind]Contract{
		InvalidKey:                   {Status: 401, Code: "invalid_key"},
		OrganizationNotAllowed:       {Status: 403, Code: "organization_not_allowed"},
		AuthUnavailable:              {Status: 503, Code: "auth_unavailable", RetryAfter: true},
		RoutingPolicyUnavailable:     {Status: 503, Code: "routing_policy_unavailable", RetryAfter: true},
		TestScopeRejected:            {Status: 403, Code: "test_scope_rejected"},
		TestIdentityMismatch:         {Status: 403, Code: "test_identity_mismatch"},
		AdminLoginDisabled:           {Status: 503, Code: "admin_login_disabled"},
		AdminSessionRequired:         {Status: 401, Code: "admin_session_required"},
		BillingUnavailable:           {Status: 503, Code: "billing_unavailable"},
		InsufficientCredits:          {Status: 402, Code: "insufficient_credits"},
		TestBudgetUnavailable:        {Status: 503, Code: "test_budget_unavailable"},
		TestBudgetDepleted:           {Status: 402, Code: "test_budget_depleted"},
		KeySpendCapReached:           {Status: 402, Code: "key_spend_cap_reached"},
		DelegatedSpendCapReached:     {Status: 402, Code: "key_spend_cap_reached"},
		OrgMonthlySpendLimitReached:  {Status: 402, Code: "org_monthly_spend_limit_reached"},
		PolicyPinMalformed:           {Status: 400, Code: "policy_pin_malformed"},
		ProductIneligible:            {Status: 403, Code: "product_ineligible"},
		SubscriptionClientAppRefused: {Status: 403, Code: "subscription_client_app_refused"},
		CreditsExhausted:             {Status: 402, Code: "credits_exhausted"},
		SpendCapReached:              {Status: 402, Code: "spend_cap_reached"},
		WeaveCreditsDisabled:         {Status: 402, Code: "weave_credits_disabled", NoClientRetry: true},
		ClaudeEnterpriseOnly:         {Status: 402, Code: "claude_enterprise_only", NoClientRetry: true},
		SubscriptionPoolExhausted:    {Status: 429, Code: "subscription_pool_exhausted", RetryAfter: true},
		SubscriptionPoolThrottled:    {Status: 429, Code: "subscription_pool_throttled", RetryAfter: true},
		SubscriptionPoolUnavailable:  {Status: 503, Code: "subscription_pool_unavailable", RetryAfter: true},
		UserSpendLimitReached:        {Status: 402, Code: "user_spend_limit_reached"},
		SpendLimitUnavailable:        {Status: 503, Code: "spend_limit_unavailable", RetryAfter: true},
		PolicyPinUnavailable:         {Status: 503, Code: "policy_pin_unavailable"},
	}
	assert.Len(t, want, int(kindCount), "every kind must be pinned here")
	for kind, contract := range want {
		assert.Equal(t, contract, kind.Contract(), "kind %d", kind)
	}
}
