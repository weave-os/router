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
		assert.NotEmpty(t, contract.Message, "kind %d has no message in the refusal table", kind)
		assert.False(t, contract.RetryAfter && contract.NoClientRetry, "kind %d both asks for and forbids a retry", kind)
		if contract.Status == http.StatusPaymentRequired {
			assert.True(t, contract.NoClientRetry, "kind %d is a 402 that clients may retry", kind)
		}
	}
}

type wireContract struct {
	Status        int
	Code          string
	RetryAfter    bool
	NoClientRetry bool
}

// TestContractTable pins the full refusal table; changing a row is a
// behavior change for every site that refuses with that kind.
func TestContractTable(t *testing.T) {
	want := map[Kind]wireContract{
		InvalidKey:                     {Status: 401, Code: "invalid_key"},
		OrganizationNotAllowed:         {Status: 403, Code: "organization_not_allowed"},
		AuthUnavailable:                {Status: 503, Code: "auth_unavailable", RetryAfter: true},
		RoutingPolicyUnavailable:       {Status: 503, Code: "routing_policy_unavailable", RetryAfter: true},
		TestScopeRejected:              {Status: 403, Code: "test_scope_rejected"},
		TestIdentityMismatch:           {Status: 403, Code: "test_identity_mismatch"},
		AdminLoginDisabled:             {Status: 503, Code: "admin_login_disabled"},
		AdminSessionRequired:           {Status: 401, Code: "admin_session_required"},
		BillingUnavailable:             {Status: 503, Code: "billing_unavailable", RetryAfter: true},
		InsufficientCredits:            {Status: 402, Code: "insufficient_credits", NoClientRetry: true},
		TestBudgetUnavailable:          {Status: 503, Code: "test_budget_unavailable"},
		TestBudgetDepleted:             {Status: 402, Code: "test_budget_depleted", NoClientRetry: true},
		KeySpendCapReached:             {Status: 402, Code: "key_spend_cap_reached", NoClientRetry: true},
		DelegatedSpendCapReached:       {Status: 402, Code: "delegated_spend_cap_reached", NoClientRetry: true},
		OrgMonthlySpendLimitReached:    {Status: 402, Code: "org_monthly_spend_limit_reached", NoClientRetry: true},
		PolicyPinMalformed:             {Status: 400, Code: "policy_pin_malformed"},
		ProductIneligible:              {Status: 403, Code: "product_ineligible"},
		SubscriptionClientAppRefused:   {Status: 403, Code: "subscription_client_app_refused"},
		CreditsExhausted:               {Status: 402, Code: "credits_exhausted", NoClientRetry: true},
		SpendCapReached:                {Status: 402, Code: "spend_cap_reached", NoClientRetry: true},
		WeaveCreditsDisabled:           {Status: 402, Code: "weave_credits_disabled", NoClientRetry: true},
		ClaudeEnterpriseOnly:           {Status: 402, Code: "claude_enterprise_only", NoClientRetry: true},
		SubscriptionPoolExhausted:      {Status: 429, Code: "subscription_pool_exhausted", RetryAfter: true},
		SubscriptionPoolThrottled:      {Status: 429, Code: "subscription_pool_throttled", RetryAfter: true},
		SubscriptionPoolUnavailable:    {Status: 503, Code: "subscription_pool_unavailable", RetryAfter: true},
		UserSpendLimitReached:          {Status: 402, Code: "user_spend_limit_reached", NoClientRetry: true},
		SpendLimitUnavailable:          {Status: 503, Code: "spend_limit_unavailable", RetryAfter: true},
		PolicyPinUnavailable:           {Status: 503, Code: "policy_pin_unavailable", RetryAfter: true},
		ServingAdmissionRejected:       {Status: 403, Code: "serving_admission_rejected"},
		ServingAdmissionUnavailable:    {Status: 503, Code: "serving_admission_unavailable", RetryAfter: true},
		ServingSnapshotUnavailable:     {Status: 503, Code: "serving_snapshot_unavailable"},
		ServingAttributionUnavailable:  {Status: 503, Code: "serving_attribution_unavailable"},
		SubscriptionUnavailableForTurn: {Status: 402, Code: "subscription_unavailable_for_turn", NoClientRetry: true},
	}
	assert.Len(t, want, int(kindCount), "every kind must be pinned here")
	for kind, expected := range want {
		contract := kind.Contract()
		assert.Equal(t, expected, wireContract{Status: contract.Status, Code: contract.Code, RetryAfter: contract.RetryAfter, NoClientRetry: contract.NoClientRetry}, "kind %d", kind)
	}
}

// TestNativeErrorTypes pins the error type each API format reports for every
// refusal status the table uses.
func TestNativeErrorTypes(t *testing.T) {
	for status, want := range map[int]struct{ anthropic, openai, gemini string }{
		400: {"invalid_request_error", "invalid_request_error", "INVALID_ARGUMENT"},
		401: {"authentication_error", "invalid_request_error", "UNAUTHENTICATED"},
		402: {"billing_error", "api_error", "FAILED_PRECONDITION"},
		403: {"permission_error", "invalid_request_error", "PERMISSION_DENIED"},
		429: {"rate_limit_error", "api_error", "RESOURCE_EXHAUSTED"},
		503: {"api_error", "api_error", "UNAVAILABLE"},
	} {
		contract := Contract{Status: status}
		assert.Equal(t, want.anthropic, contract.AnthropicType(), "status %d", status)
		assert.Equal(t, want.openai, contract.OpenAIType(), "status %d", status)
		assert.Equal(t, want.gemini, contract.GeminiStatus(), "status %d", status)
	}
	for _, kind := range Kinds() {
		assert.NotEqual(t, "INTERNAL", kind.Contract().GeminiStatus(), "kind %d has a status with no Gemini mapping", kind)
	}
}
