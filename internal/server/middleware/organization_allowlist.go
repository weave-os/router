package middleware

import (
	"context"
	"errors"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/policyregistry"
)

// inferenceOrganizationAllowlistEnv restricts a deployment to the listed
// organizations so shared non-production fleets cannot spend provider keys on
// self-serve signups. Unset admits every organization.
const inferenceOrganizationAllowlistEnv = "ROUTER_INFERENCE_ORGANIZATION_ALLOWLIST"

var errOrganizationNotAllowed = errors.New("organization is not allowed on this deployment")

type organizationAllowlist map[string]struct{}

func organizationAllowlistFromEnv() organizationAllowlist {
	var allowed organizationAllowlist
	for _, externalID := range strings.Split(config.GetOr(inferenceOrganizationAllowlistEnv, ""), ",") {
		if externalID = strings.TrimSpace(externalID); externalID == "" {
			continue
		}
		if allowed == nil {
			allowed = organizationAllowlist{}
		}
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

// verifyCredential resolves the bearer credential. Signed internal test plans
// were already admitted by the policy registry, so the allowlist skips them.
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
