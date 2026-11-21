package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

type Caller struct {
	Email              string
	AccountID          string
	DisplayName        string
	SessionID          string
	CredentialIdentity string
	InternalTest       bool
}

func (c Caller) credentialIdentity(apiKeyID string) string {
	if c.CredentialIdentity != "" {
		return c.CredentialIdentity
	}
	return apiKeyID
}

func ResolveUserFromContext(ctx context.Context, authSvc *Service, installation *Installation, apiKey *APIKey, caller Caller) context.Context {
	return withBlindExperimentSession(resolveUserFromContext(ctx, authSvc, installation, apiKey, caller), authSvc, installation, apiKey, caller)
}

func withBlindExperimentSession(ctx context.Context, authSvc *Service, installation *Installation, apiKey *APIKey, caller Caller) context.Context {
	if authSvc == nil || installation == nil {
		return ctx
	}
	if caller.InternalTest {
		return ctx
	}
	apiKeyID := ""
	if apiKey != nil {
		apiKeyID = apiKey.ID
	}
	sessionKey := BlindExperimentSessionSubjectKey(caller.credentialIdentity(apiKeyID), caller.SessionID)
	return authSvc.WithBlindExperimentSession(ctx, installation.ID, sessionKey)
}

func resolveUserFromContext(ctx context.Context, authSvc *Service, installation *Installation, apiKey *APIKey, caller Caller) context.Context {
	if caller.InternalTest {
		return ctx
	}
	log := observability.FromContext(ctx)
	if authSvc == nil || installation == nil {
		log.Info("ResolveUserFromContext bailout",
			"reason", "nil_dep",
			"authsvc_nil", authSvc == nil,
			"installation_nil", installation == nil,
		)
		return ctx
	}
	ctx = observability.WithClientSessionID(ctx, caller.SessionID)
	if caller.Email == "" && caller.AccountID == "" {
		if apiKey != nil && apiKey.CredentialSubjectID != "" {
			return authSvc.ResolveAndStashKeyOwner(ctx, installation.ID, apiKey.CredentialSubjectID, caller.DisplayName)
		}
		if apiKey != nil && apiKey.ID != "" {
			return authSvc.ResolveAndStashUnidentifiedKeyUser(ctx, installation.ID, apiKey.ID)
		}
		log.Info("ResolveUserFromContext bailout",
			"reason", "no_identity_signal",
			"installation_id", installation.ID,
		)
		return ctx
	}
	log.Debug("ResolveUserFromContext dispatch",
		"installation_id", installation.ID,
		"email_present", caller.Email != "",
		"account_present", caller.AccountID != "",
		"name_present", caller.DisplayName != "",
	)
	return authSvc.ResolveAndStashUser(ctx, installation.ID, caller.Email, caller.AccountID, caller.DisplayName)
}

func ResolveRoutingAssignment(ctx context.Context, authSvc *Service, installation *Installation, caller Caller) (context.Context, error) {
	if caller.InternalTest {
		return ctx, nil
	}
	if authSvc == nil || installation == nil || RoutingPolicyFrom(ctx).Mode != RoutingPolicyAssigned {
		return ctx, nil
	}
	routerUserID, _ := ctx.Value(UserIDContextKey{}).(string)
	if routerUserID == "" && (caller.Email != "" || caller.AccountID != "") {
		return ctx, ErrRoutingPolicyUnavailable
	}
	return authSvc.WithRoutingAssignment(ctx, installation.ID, routerUserID)
}
