// Package refusal is the single contract for how the router refuses a request
// for auth, billing, entitlement and policy reasons: each Kind maps to exactly
// one HTTP status, machine-readable code and retry signal. Refusal sites name a
// Kind and never pick a status themselves.
package refusal

import "net/http"

// Kind identifies one reason the router refuses a request.
type Kind int

const (
	InvalidKey Kind = iota
	OrganizationNotAllowed
	AuthUnavailable
	RoutingPolicyUnavailable
	TestScopeRejected
	TestIdentityMismatch
	AdminLoginDisabled
	AdminSessionRequired
	BillingUnavailable
	InsufficientCredits
	TestBudgetUnavailable
	TestBudgetDepleted
	KeySpendCapReached
	DelegatedSpendCapReached
	OrgMonthlySpendLimitReached
	PolicyPinMalformed
	ProductIneligible
	SubscriptionClientAppRefused
	CreditsExhausted
	SpendCapReached
	WeaveCreditsDisabled
	ClaudeEnterpriseOnly
	SubscriptionPoolExhausted
	SubscriptionPoolThrottled
	SubscriptionPoolUnavailable
	UserSpendLimitReached
	SpendLimitUnavailable
	PolicyPinUnavailable
	kindCount
)

// Contract is the observable result of a refusal.
type Contract struct {
	Status int
	// Code names the refusal on the wire where the envelope carries a code.
	Code string
	// RetryAfter means the response carries Retry-After: 1.
	RetryAfter bool
	// NoClientRetry means the response carries x-should-retry: false, telling
	// SDK clients the identical request cannot succeed.
	NoClientRetry bool
}

var contracts = [kindCount]Contract{
	InvalidKey:                   {Status: http.StatusUnauthorized, Code: "invalid_key"},
	OrganizationNotAllowed:       {Status: http.StatusForbidden, Code: "organization_not_allowed"},
	AuthUnavailable:              {Status: http.StatusServiceUnavailable, Code: "auth_unavailable", RetryAfter: true},
	RoutingPolicyUnavailable:     {Status: http.StatusServiceUnavailable, Code: "routing_policy_unavailable", RetryAfter: true},
	TestScopeRejected:            {Status: http.StatusForbidden, Code: "test_scope_rejected"},
	TestIdentityMismatch:         {Status: http.StatusForbidden, Code: "test_identity_mismatch"},
	AdminLoginDisabled:           {Status: http.StatusServiceUnavailable, Code: "admin_login_disabled"},
	AdminSessionRequired:         {Status: http.StatusUnauthorized, Code: "admin_session_required"},
	BillingUnavailable:           {Status: http.StatusServiceUnavailable, Code: "billing_unavailable"},
	InsufficientCredits:          {Status: http.StatusPaymentRequired, Code: "insufficient_credits"},
	TestBudgetUnavailable:        {Status: http.StatusServiceUnavailable, Code: "test_budget_unavailable"},
	TestBudgetDepleted:           {Status: http.StatusPaymentRequired, Code: "test_budget_depleted"},
	KeySpendCapReached:           {Status: http.StatusPaymentRequired, Code: "key_spend_cap_reached"},
	DelegatedSpendCapReached:     {Status: http.StatusPaymentRequired, Code: "key_spend_cap_reached"},
	OrgMonthlySpendLimitReached:  {Status: http.StatusPaymentRequired, Code: "org_monthly_spend_limit_reached"},
	PolicyPinMalformed:           {Status: http.StatusBadRequest, Code: "policy_pin_malformed"},
	ProductIneligible:            {Status: http.StatusForbidden, Code: "product_ineligible"},
	SubscriptionClientAppRefused: {Status: http.StatusForbidden, Code: "subscription_client_app_refused"},
	CreditsExhausted:             {Status: http.StatusPaymentRequired, Code: "credits_exhausted"},
	SpendCapReached:              {Status: http.StatusPaymentRequired, Code: "spend_cap_reached"},
	WeaveCreditsDisabled:         {Status: http.StatusPaymentRequired, Code: "weave_credits_disabled", NoClientRetry: true},
	ClaudeEnterpriseOnly:         {Status: http.StatusPaymentRequired, Code: "claude_enterprise_only", NoClientRetry: true},
	SubscriptionPoolExhausted:    {Status: http.StatusTooManyRequests, Code: "subscription_pool_exhausted", RetryAfter: true},
	SubscriptionPoolThrottled:    {Status: http.StatusTooManyRequests, Code: "subscription_pool_throttled", RetryAfter: true},
	SubscriptionPoolUnavailable:  {Status: http.StatusServiceUnavailable, Code: "subscription_pool_unavailable", RetryAfter: true},
	UserSpendLimitReached:        {Status: http.StatusPaymentRequired, Code: "user_spend_limit_reached"},
	SpendLimitUnavailable:        {Status: http.StatusServiceUnavailable, Code: "spend_limit_unavailable", RetryAfter: true},
	PolicyPinUnavailable:         {Status: http.StatusServiceUnavailable, Code: "policy_pin_unavailable"},
}

// Contract returns the kind's entry in the refusal table.
func (k Kind) Contract() Contract {
	return contracts[k]
}

// Kinds lists every refusal kind.
func Kinds() []Kind {
	kinds := make([]Kind, 0, kindCount)
	for k := range kindCount {
		kinds = append(kinds, k)
	}
	return kinds
}
