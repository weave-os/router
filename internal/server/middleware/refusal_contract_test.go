package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubTestBudgetBook struct {
	balance int64
	err     error
}

func (b stubTestBudgetBook) Balance(context.Context, billing.Owner) (int64, error) {
	return b.balance, b.err
}

func (stubTestBudgetBook) Debit(context.Context, billing.PrepaidDebit) (int64, error) {
	return 0, nil
}

func runTestBudgetCheck(t *testing.T, book stubTestBudgetBook) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := billing.NewService(&stubBillingRepo{}).WithInternalTestPrepaid(book)
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Request = c.Request.WithContext(requestcontext.WithInternalTestIdentity(c.Request.Context(), requestcontext.InternalTestIdentity{SubjectID: "subject-1"}))
		middleware.WithBalanceCheck(svc, 0)(c)
		if !c.IsAborted() {
			c.Status(http.StatusOK)
		}
	})
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil))
	return rr
}

func runSubscriberGate(t *testing.T, gate func(*entitlement.Service) gin.HandlerFunc, entitlements *stubEntitlements, allowances *stubAllowances) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		gate(svc)(c)
		if !c.IsAborted() {
			c.Status(http.StatusOK)
		}
	})
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil))
	return rr
}

func runAuthWithRepo(t *testing.T, repo *fakeAPIKeyRepository, token string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.POST("/v1/messages", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	req.Header.Set(middleware.RouterKeyHeader, token)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func runMalformedPolicyPin(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_installation", overrideEnabledInstallation())
		c.Next()
	})
	engine.Use(middleware.WithPolicyPinOverride())
	engine.POST(path, func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
	req.Header.Set(middleware.PolicyPinOverrideHeader, "not-a-pin")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

// TestRefusalContract pins the observable result of every middleware refusal
// site: status, error code, the body shape it travels in and the retry headers.
var errRefusalBillingDown = errors.New("billing down")

type refusalScenario struct {
	name       string
	run        func(t *testing.T) *httptest.ResponseRecorder
	status     int
	body       string
	retryAfter string
}

func authAndBalanceRefusals() []refusalScenario {
	return []refusalScenario{
		{
			name: "auth invalid key",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runAuthWithRepo(t, &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}, "rk_no_such_key")
			},
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_key"}`,
		},
		{
			name: "auth unavailable",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				const token = "rk_valid_but_db_is_down"
				hash, _, _ := auth.APITokenFingerprint(token)
				return runAuthWithRepo(t, &fakeAPIKeyRepository{
					byHash:    map[string]fakeKeyRow{hash: {apiKey: &auth.APIKey{ID: "key-1"}, installation: &auth.Installation{ID: "inst-1"}}},
					lookupErr: context.DeadlineExceeded,
				}, token)
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"error":"auth_unavailable"}`,
			retryAfter: "1",
		},
		{
			name: "balance depleted",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runMiddleware(t, &stubBillingRepo{balance: 0}, 0)
				return rr
			},
			status: http.StatusPaymentRequired,
			body:   `{"error":"insufficient_credits","top_up_url":"` + middleware.TopUpURL + `","balance_usd_micros":0,"message":"Your organization's prepaid credits are depleted. Contact your org admin to add credits."}`,
		},
		{
			name: "balance read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runMiddleware(t, &stubBillingRepo{balanceErr: errRefusalBillingDown}, 0)
				return rr
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
		{
			name: "internal test budget depleted",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runTestBudgetCheck(t, stubTestBudgetBook{balance: 0})
			},
			status: http.StatusPaymentRequired,
			body:   `{"error":"test_budget_depleted"}`,
		},
		{
			name: "internal test budget read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runTestBudgetCheck(t, stubTestBudgetBook{err: errRefusalBillingDown})
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"test_budget_unavailable"}`,
		},
	}
}

func capAndSubscriberRefusals() []refusalScenario {
	return []refusalScenario{
		{
			name: "api key spend cap reached",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runSpendCap(t, "key-1", &stubBillingRepo{spendMicros: 10, capMicros: capPtr(10), spendFound: true})
				return rr
			},
			status: http.StatusPaymentRequired,
			body:   `{"error":"key_spend_cap_reached","spent_usd_micros":10,"spend_cap_usd_micros":10,"message":"This router key has reached its spend cap. Mint a new key or raise the cap to continue."}`,
		},
		{
			name: "api key spend cap read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runSpendCap(t, "key-1", &stubBillingRepo{spendErr: errRefusalBillingDown})
				return rr
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
		{
			name: "org monthly spend limit reached",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runOrgMonthlyCap(t, "org_x", &stubBillingRepo{orgMonthSpent: 10, orgMonthLimit: capPtr(10)})
				return rr
			},
			status: http.StatusPaymentRequired,
			body:   `{"error":"org_monthly_spend_limit_reached","spent_usd_micros":10,"monthly_limit_usd_micros":10,"message":"Your organization has reached its monthly Weave Router spend limit. An org admin can raise the limit, or it resets next month."}`,
		},
		{
			name: "org monthly spend limit read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runOrgMonthlyCap(t, "org_x", &stubBillingRepo{orgMonthErr: errRefusalBillingDown})
				return rr
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
		{
			name: "delegated spend cap reached",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runDelegatedSpendCap(t, &stubDelegatedSpend{sessionSpent: 10}, &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: capPtr(10)})
				return rr
			},
			status: http.StatusPaymentRequired,
		},
		{
			name: "delegated spend cap read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runDelegatedSpendCap(t, &stubDelegatedSpend{err: errRefusalBillingDown}, &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: capPtr(10)})
				return rr
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
		{
			name: "subscriber product scope read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runSubscriberGate(t, middleware.WithSubscriberProductScope, &stubEntitlements{err: errRefusalBillingDown}, &stubAllowances{})
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
		{
			name: "subscriber allowance read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runSubscriberGate(t, middleware.WithSubscriberAllowance, &stubEntitlements{err: errRefusalBillingDown}, &stubAllowances{})
			},
			status: http.StatusServiceUnavailable,
			body:   `{"error":"billing_unavailable","message":"Billing system is temporarily unavailable. Retry in a few moments."}`,
		},
	}
}

func TestRefusalContract(t *testing.T) {
	scenarios := append(authAndBalanceRefusals(), capAndSubscriberRefusals()...)
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			rr := scenario.run(t)
			require.Equal(t, scenario.status, rr.Code)
			if scenario.body != "" {
				assert.JSONEq(t, scenario.body, rr.Body.String())
			}
			assert.Equal(t, scenario.retryAfter, rr.Header().Get("Retry-After"))
			assert.Empty(t, rr.Header().Get("x-should-retry"))
		})
	}
}

func TestRefusalContract_DelegatedSpendCapBody(t *testing.T) {
	rr, _ := runDelegatedSpendCap(t, &stubDelegatedSpend{sessionSpent: 10}, &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: capPtr(10)})
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "key_spend_cap_reached", body["error"])
	assert.Equal(t, "session", body["scope"])
	assert.InDelta(t, 10, body["spent_usd_micros"], 0)
	assert.InDelta(t, 10, body["spend_cap_usd_micros"], 0)
	assert.NotEmpty(t, body["message"])
}

// TestRefusalContract_MalformedPolicyPinPerFormat pins the one middleware
// refusal that answers in each API's native error envelope.
func TestRefusalContract_MalformedPolicyPinPerFormat(t *testing.T) {
	for _, scenario := range []struct {
		path string
		body string
	}{
		{path: "/v1/messages", body: `{"type":"error","error":{"type":"invalid_request_error","code":"policy_pin_malformed","message":"MESSAGE"}}`},
		{path: "/v1/chat/completions", body: `{"error":{"type":"invalid_request_error","message":"MESSAGE","param":null,"code":"policy_pin_malformed"}}`},
		{path: "/v1/responses", body: `{"error":{"type":"invalid_request_error","message":"MESSAGE","param":null,"code":"policy_pin_malformed"}}`},
		{path: "/v1beta/models/gemini:generateContent", body: `{"error":{"code":400,"message":"MESSAGE","status":"INVALID_ARGUMENT","details":[{"reason":"policy_pin_malformed"}]}}`},
	} {
		t.Run(scenario.path, func(t *testing.T) {
			rr := runMalformedPolicyPin(t, scenario.path)
			require.Equal(t, http.StatusBadRequest, rr.Code)
			assert.JSONEq(t, scenario.body, withRefusalMessage(t, rr.Body.Bytes()))
			assert.Empty(t, rr.Header().Get("Retry-After"))
		})
	}
}

// withRefusalMessage replaces the human-readable message so the pin covers
// the envelope shape and codes, not message wording.
func withRefusalMessage(t *testing.T, raw []byte) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	inner, ok := body["error"].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, inner["message"])
	inner["message"] = "MESSAGE"
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return string(out)
}
