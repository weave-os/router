package middleware

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
)

var errOrganizationNotAllowed = errors.New("organization is not allowed on this deployment")

type Config struct {
	InferenceOrganizationAllowlist []string
}

var activeConfig atomic.Pointer[Config]

func Configure(cfg Config) {
	activeConfig.Store(&cfg)
}

func ParseOrganizationAllowlist(raw string) []string {
	var externalIDs []string
	for _, externalID := range strings.Split(raw, ",") {
		if externalID = strings.TrimSpace(externalID); externalID != "" {
			externalIDs = append(externalIDs, externalID)
		}
	}
	return externalIDs
}

type organizationAllowlist map[string]struct{}

func configuredOrganizationAllowlist() organizationAllowlist {
	cfg := activeConfig.Load()
	if cfg == nil || len(cfg.InferenceOrganizationAllowlist) == 0 {
		return nil
	}
	allowed := organizationAllowlist{}
	for _, externalID := range cfg.InferenceOrganizationAllowlist {
		allowed[externalID] = struct{}{}
	}
	return allowed
}

func (a organizationAllowlist) admit(installation *auth.Installation) error {
	if a == nil {
		return nil
	}
	if _, ok := a[installation.ExternalID]; ok {
		return nil
	}
	return errOrganizationNotAllowed
}

func verifyCredential(ctx context.Context, svc *auth.Service, token string, testPlan *policyregistry.TestPlanScope, allowed organizationAllowlist) (installation *auth.Installation, apiKey *auth.APIKey, externalKeys []*auth.ExternalAPIKey, clusterModelLists []auth.ClusterModelList, delegatedSession *auth.DelegatedSession, err error) {
	switch {
	case auth.IsSessionToken(token):
		installation, apiKey, externalKeys, clusterModelLists, delegatedSession, err = svc.VerifySessionToken(ctx, token, testPlan == nil)
	case testPlan != nil:
		installation, apiKey, clusterModelLists, err = svc.VerifyPlatformAPIKey(ctx, token)
	default:
		installation, apiKey, externalKeys, clusterModelLists, err = svc.VerifyAPIKey(ctx, token)
	}
	if err == nil && testPlan == nil {
		err = allowed.admit(installation)
	}
	return installation, apiKey, externalKeys, clusterModelLists, delegatedSession, err
}
