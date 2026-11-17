package refusal

import "net/http"

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
	ServingAdmissionRejected
	ServingAdmissionUnavailable
	ServingSnapshotUnavailable
	ServingAttributionUnavailable
	SubscriptionUnavailableForTurn
	RequestBodyBudgetExhausted
	kindCount
)

type Contract struct {
	Status        int
	Code          string
	RetryAfter    bool
	NoClientRetry bool
	Message       string
}

var contracts = [kindCount]Contract{
	InvalidKey:                     {Status: http.StatusUnauthorized, Code: "invalid_key", Message: "Invalid or missing Router API key."},
	OrganizationNotAllowed:         {Status: http.StatusForbidden, Code: "organization_not_allowed", Message: "This organization is not allowed to use this router."},
	AuthUnavailable:                {Status: http.StatusServiceUnavailable, Code: "auth_unavailable", RetryAfter: true, Message: "Authentication is temporarily unavailable. Retry in a few moments."},
	RoutingPolicyUnavailable:       {Status: http.StatusServiceUnavailable, Code: "routing_policy_unavailable", RetryAfter: true, Message: "Routing policy temporarily unavailable."},
	TestScopeRejected:              {Status: http.StatusForbidden, Code: "test_scope_rejected", Message: "This test credential is not valid for this request."},
	TestIdentityMismatch:           {Status: http.StatusForbidden, Code: "test_identity_mismatch", Message: "The test identity does not match this credential."},
	AdminLoginDisabled:             {Status: http.StatusServiceUnavailable, Code: "admin_login_disabled", Message: "Admin login is disabled on this router."},
	AdminSessionRequired:           {Status: http.StatusUnauthorized, Code: "admin_session_required", Message: "Sign in to the admin dashboard to continue."},
	BillingUnavailable:             {Status: http.StatusServiceUnavailable, Code: "billing_unavailable", RetryAfter: true, Message: "Billing system is temporarily unavailable. Retry in a few moments."},
	InsufficientCredits:            {Status: http.StatusPaymentRequired, Code: "insufficient_credits", NoClientRetry: true, Message: "Your organization's prepaid credits are depleted."},
	TestBudgetUnavailable:          {Status: http.StatusServiceUnavailable, Code: "test_budget_unavailable", Message: "The test budget is temporarily unavailable."},
	TestBudgetDepleted:             {Status: http.StatusPaymentRequired, Code: "test_budget_depleted", NoClientRetry: true, Message: "This test budget is depleted."},
	KeySpendCapReached:             {Status: http.StatusPaymentRequired, Code: "key_spend_cap_reached", NoClientRetry: true, Message: "This router key has reached its spend cap."},
	DelegatedSpendCapReached:       {Status: http.StatusPaymentRequired, Code: "delegated_spend_cap_reached", NoClientRetry: true, Message: "This session has reached its spend cap."},
	OrgMonthlySpendLimitReached:    {Status: http.StatusPaymentRequired, Code: "org_monthly_spend_limit_reached", NoClientRetry: true, Message: "Your organization has reached its monthly Weave Router spend limit."},
	PolicyPinMalformed:             {Status: http.StatusBadRequest, Code: "policy_pin_malformed", Message: "The policy pin header is malformed."},
	ProductIneligible:              {Status: http.StatusForbidden, Code: "product_ineligible", Message: "This model is not included in your plan."},
	SubscriptionClientAppRefused:   {Status: http.StatusForbidden, Code: "subscription_client_app_refused", Message: "This app can't use Claude subscription plan limits."},
	CreditsExhausted:               {Status: http.StatusPaymentRequired, Code: "credits_exhausted", NoClientRetry: true, Message: "Your Weave router credits are exhausted."},
	SpendCapReached:                {Status: http.StatusPaymentRequired, Code: "spend_cap_reached", NoClientRetry: true, Message: "A Weave router spend cap has been reached."},
	WeaveCreditsDisabled:           {Status: http.StatusPaymentRequired, Code: "weave_credits_disabled", NoClientRetry: true, Message: "Weave credits are turned off for your organization."},
	ClaudeEnterpriseOnly:           {Status: http.StatusPaymentRequired, Code: "claude_enterprise_only", NoClientRetry: true, Message: "No usable Claude Enterprise account can serve this request."},
	SubscriptionPoolExhausted:      {Status: http.StatusTooManyRequests, Code: "subscription_pool_exhausted", RetryAfter: true, Message: "All enrolled subscription accounts are currently unavailable."},
	SubscriptionPoolThrottled:      {Status: http.StatusTooManyRequests, Code: "subscription_pool_throttled", RetryAfter: true, Message: "All enrolled subscription accounts are rate-limited."},
	SubscriptionPoolUnavailable:    {Status: http.StatusServiceUnavailable, Code: "subscription_pool_unavailable", RetryAfter: true, Message: "Subscription account service is temporarily unavailable."},
	UserSpendLimitReached:          {Status: http.StatusPaymentRequired, Code: "user_spend_limit_reached", NoClientRetry: true, Message: "You've reached your monthly Weave Router spend limit."},
	SpendLimitUnavailable:          {Status: http.StatusServiceUnavailable, Code: "spend_limit_unavailable", RetryAfter: true, Message: "Billing system is temporarily unavailable. Retry in a few moments."},
	PolicyPinUnavailable:           {Status: http.StatusServiceUnavailable, Code: "policy_pin_unavailable", RetryAfter: true, Message: "The pinned policy is not loaded on this router."},
	ServingAdmissionRejected:       {Status: http.StatusForbidden, Code: "serving_admission_rejected", Message: "This request was not admitted to this serving fleet."},
	ServingAdmissionUnavailable:    {Status: http.StatusServiceUnavailable, Code: "serving_admission_unavailable", RetryAfter: true, Message: "Serving admission is temporarily unavailable."},
	ServingSnapshotUnavailable:     {Status: http.StatusServiceUnavailable, Code: "serving_snapshot_unavailable", Message: "The admitted serving snapshot is unavailable."},
	ServingAttributionUnavailable:  {Status: http.StatusServiceUnavailable, Code: "serving_attribution_unavailable", Message: "Request attribution is unavailable."},
	SubscriptionUnavailableForTurn: {Status: http.StatusPaymentRequired, Code: "subscription_unavailable_for_turn", NoClientRetry: true, Message: "Your subscription can't serve this turn."},
	RequestBodyBudgetExhausted:     {Status: http.StatusServiceUnavailable, Code: "request_body_budget_exhausted", RetryAfter: true, Message: "This router is processing too many large requests. Retry in a few moments."},
}

func (k Kind) Contract() Contract {
	return contracts[k]
}

func Kinds() []Kind {
	kinds := make([]Kind, 0, kindCount)
	for k := range kindCount {
		kinds = append(kinds, k)
	}
	return kinds
}

func (c Contract) AnthropicType() string {
	switch c.Status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func (c Contract) OpenAIType() string {
	switch c.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

func (c Contract) GeminiStatus() string {
	switch c.Status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusPaymentRequired:
		return "FAILED_PRECONDITION"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}
