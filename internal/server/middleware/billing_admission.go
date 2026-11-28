package middleware

import (
	"github.com/gin-gonic/gin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
)

const admissionStateKey = "router_billing_admission"

type admissionResult struct {
	billing.Admission
	err error
}

func loadBillingAdmission(c *gin.Context, svc *billing.Service, orgID string) {
	reader := svc.AdmissionReader()
	if reader == nil {
		return
	}
	scope := billing.AdmissionScope{OrganizationID: orgID}
	if key := APIKeyFrom(c); key != nil {
		scope.KeyID = key.ID
	}
	if s := auth.DelegatedSessionFrom(c.Request.Context()); s != nil && (s.TaskCapMicros != nil || s.SessionCapMicros != nil) {
		scope.Delegated = true
		scope.IssuerID = s.IssuerKeyID
		scope.SessionID = s.SessionID
		scope.TaskID = s.TaskID
	}
	state, err := reader.ReadAdmission(c.Request.Context(), scope)
	c.Set(admissionStateKey, &admissionResult{state, err})
}

func admissionState(c *gin.Context) *admissionResult {
	value, _ := c.Get(admissionStateKey)
	state, _ := value.(*admissionResult)
	return state
}

func admissionBalance(c *gin.Context, svc *billing.Service, orgID string) (billing.CheckResult, error) {
	if state := admissionState(c); state != nil {
		if state.err == nil && !state.Balance.HasOverride && !state.BalanceFound {
			return billing.CheckResult{}, billing.ErrBalanceRowMissing
		}
		return state.Balance, state.err
	}
	return svc.CheckBalance(c.Request.Context(), orgID)
}

func admissionKey(c *gin.Context, svc *billing.Service, keyID string) (billing.APIKeySpendCapResult, error) {
	if state := admissionState(c); state != nil {
		return state.Key, state.err
	}
	return svc.CheckAPIKeySpendCap(c.Request.Context(), keyID)
}

func admissionMonthly(c *gin.Context, svc *billing.Service, orgID string) (billing.MonthlySpendResult, error) {
	if state := admissionState(c); state != nil {
		return state.Monthly, state.err
	}
	return svc.CheckOrgMonthlySpend(c.Request.Context(), orgID)
}

func admissionDelegated(c *gin.Context, svc *billing.Service, s *auth.DelegatedSession) (int64, int64, error) {
	if state := admissionState(c); state != nil {
		if state.err != nil {
			return 0, 0, state.err
		}
		return state.SessionSpent, state.TaskSpent, state.DelegatedErr
	}
	return svc.CheckDelegatedSpend(c.Request.Context(), s.IssuerKeyID, s.SessionID, s.TaskID)
}
