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
	return runAuthAt(t, repo, token, "/v1/messages")
}

func runAuthAt(t *testing.T, repo *fakeAPIKeyRepository, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.Any(path, func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
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
// site on an inference route: status, the native error envelope with its code,
// and the retry headers.
var errRefusalBillingDown = errors.New("billing down")

type refusalScenario struct {
	name        string
	run         func(t *testing.T) *httptest.ResponseRecorder
	status      int
	body        string
	retryAfter  string
	shouldRetry string
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
			body:   `{"type":"error","error":{"type":"authentication_error","code":"invalid_key","message":"MESSAGE"}}`,
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
			body:       `{"type":"error","error":{"type":"api_error","code":"auth_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "balance depleted",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runMiddleware(t, &stubBillingRepo{balance: 0}, 0)
				return rr
			},
			status:      http.StatusPaymentRequired,
			body:        `{"type":"error","error":{"type":"billing_error","code":"insufficient_credits","message":"MESSAGE","top_up_url":"` + middleware.TopUpURL + `","balance_usd_micros":0}}`,
			shouldRetry: "false",
		},
		{
			name: "balance read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runMiddleware(t, &stubBillingRepo{balanceErr: errRefusalBillingDown}, 0)
				return rr
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "internal test budget depleted",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runTestBudgetCheck(t, stubTestBudgetBook{balance: 0})
			},
			status:      http.StatusPaymentRequired,
			body:        `{"type":"error","error":{"type":"billing_error","code":"test_budget_depleted","message":"MESSAGE"}}`,
			shouldRetry: "false",
		},
		{
			name: "internal test budget read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runTestBudgetCheck(t, stubTestBudgetBook{err: errRefusalBillingDown})
			},
			status: http.StatusServiceUnavailable,
			body:   `{"type":"error","error":{"type":"api_error","code":"test_budget_unavailable","message":"MESSAGE"}}`,
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
			status:      http.StatusPaymentRequired,
			body:        `{"type":"error","error":{"type":"billing_error","code":"key_spend_cap_reached","message":"MESSAGE","spent_usd_micros":10,"spend_cap_usd_micros":10}}`,
			shouldRetry: "false",
		},
		{
			name: "api key spend cap read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runSpendCap(t, "key-1", &stubBillingRepo{spendErr: errRefusalBillingDown})
				return rr
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "org monthly spend limit reached",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runOrgMonthlyCap(t, "org_x", &stubBillingRepo{orgMonthSpent: 10, orgMonthLimit: capPtr(10)})
				return rr
			},
			status:      http.StatusPaymentRequired,
			body:        `{"type":"error","error":{"type":"billing_error","code":"org_monthly_spend_limit_reached","message":"MESSAGE","spent_usd_micros":10,"monthly_limit_usd_micros":10}}`,
			shouldRetry: "false",
		},
		{
			name: "org monthly spend limit read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runOrgMonthlyCap(t, "org_x", &stubBillingRepo{orgMonthErr: errRefusalBillingDown})
				return rr
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "delegated spend cap reached",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runDelegatedSpendCap(t, &stubDelegatedSpend{sessionSpent: 10}, &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: capPtr(10)})
				return rr
			},
			status:      http.StatusPaymentRequired,
			body:        `{"type":"error","error":{"type":"billing_error","code":"delegated_spend_cap_reached","message":"MESSAGE","scope":"session","spent_usd_micros":10,"spend_cap_usd_micros":10}}`,
			shouldRetry: "false",
		},
		{
			name: "delegated spend cap read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				rr, _ := runDelegatedSpendCap(t, &stubDelegatedSpend{err: errRefusalBillingDown}, &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: capPtr(10)})
				return rr
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "subscriber product scope read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runSubscriberGate(t, middleware.WithSubscriberProductScope, &stubEntitlements{err: errRefusalBillingDown}, &stubAllowances{})
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
		{
			name: "subscriber allowance read failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()
				return runSubscriberGate(t, middleware.WithSubscriberAllowance, &stubEntitlements{err: errRefusalBillingDown}, &stubAllowances{})
			},
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"error","error":{"type":"api_error","code":"billing_unavailable","message":"MESSAGE"}}`,
			retryAfter: "1",
		},
	}
}

func TestRefusalContract(t *testing.T) {
	scenarios := append(authAndBalanceRefusals(), capAndSubscriberRefusals()...)
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			rr := scenario.run(t)
			require.Equal(t, scenario.status, rr.Code)
			assert.JSONEq(t, scenario.body, withRefusalMessage(t, rr.Body.Bytes()))
			assert.Equal(t, scenario.retryAfter, rr.Header().Get("Retry-After"))
			assert.Equal(t, scenario.shouldRetry, rr.Header().Get("x-should-retry"))
		})
	}
}

// TestRefusalContract_PerFormat pins the envelope a middleware refusal takes
// on each API surface; routes that are not an inference API keep the flat
// {"error": code} body that the installer reads.
func TestRefusalContract_PerFormat(t *testing.T) {
	for _, scenario := range []struct {
		path string
		body string
	}{
		{path: "/v1/messages", body: `{"type":"error","error":{"type":"authentication_error","code":"invalid_key","message":"MESSAGE"}}`},
		{path: "/v1/route", body: `{"type":"error","error":{"type":"authentication_error","code":"invalid_key","message":"MESSAGE"}}`},
		{path: "/v1/chat/completions", body: `{"error":{"type":"invalid_request_error","message":"MESSAGE","param":null,"code":"invalid_key"}}`},
		{path: "/v1/responses", body: `{"error":{"type":"invalid_request_error","message":"MESSAGE","param":null,"code":"invalid_key"}}`},
		{path: "/v1beta/models/gemini:generateContent", body: `{"error":{"code":401,"message":"MESSAGE","status":"UNAUTHENTICATED","details":[{"reason":"invalid_key"}]}}`},
	} {
		t.Run(scenario.path, func(t *testing.T) {
			rr := runAuthAt(t, &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}, "rk_no_such_key", scenario.path)
			require.Equal(t, http.StatusUnauthorized, rr.Code)
			assert.JSONEq(t, scenario.body, withRefusalMessage(t, rr.Body.Bytes()))
		})
	}
	for _, path := range []string{"/validate", "/v1/router/threads", "/v1/router/models"} {
		rr := runAuthAt(t, &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}, "rk_no_such_key", path)
		require.Equal(t, http.StatusUnauthorized, rr.Code, path)
		assert.JSONEq(t, `{"error":"invalid_key"}`, rr.Body.String(), path)
	}
}

// TestRefusalContract_MalformedPolicyPinPerFormat pins the malformed policy
// pin refusal in each API's native error envelope.
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
	require.True(t, ok, "error is not a native error envelope: %s", raw)
	require.NotEmpty(t, inner["message"])
	inner["message"] = "MESSAGE"
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return string(out)
}
