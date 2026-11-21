package proxy

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
)

type ClientIdentity = requestcontext.ClientIdentity

type ClientIdentityContextKey = requestcontext.ClientIdentityContextKey

func ClientIdentityFrom(ctx context.Context) ClientIdentity {
	return requestcontext.ClientIdentityFrom(ctx)
}

func openCodeCaller(id ClientIdentity) turntype.OpenCodeCaller {
	isClient := id.ClientApp == requestcontext.ClientAppOpencode
	return turntype.OpenCodeCaller{IsClient: isClient, IsSubagent: isClient && id.OpenCodeSubagent}
}

const EvalClientAppPrefix = requestcontext.EvalClientAppPrefix

func ResolveUserFromContext(ctx context.Context, authSvc *auth.Service, installation *auth.Installation, apiKey *auth.APIKey) context.Context {
	return withBlindExperimentSession(resolveUserFromContext(ctx, authSvc, installation, apiKey), authSvc, installation, apiKey)
}

func withBlindExperimentSession(ctx context.Context, authSvc *auth.Service, installation *auth.Installation, apiKey *auth.APIKey) context.Context {
	if authSvc == nil || installation == nil {
		return ctx
	}
	if _, internalTest := requestcontext.InternalTestIdentityFrom(ctx); internalTest {
		return ctx
	}
	apiKeyID := ""
	if apiKey != nil {
		apiKeyID = apiKey.ID
	}
	sessionKey := auth.BlindExperimentSessionSubjectKey(sessionCredentialIdentity(ctx, apiKeyID), ClientIdentityFrom(ctx).SessionID)
	return authSvc.WithBlindExperimentSession(ctx, installation.ID, sessionKey)
}

func resolveUserFromContext(ctx context.Context, authSvc *auth.Service, installation *auth.Installation, apiKey *auth.APIKey) context.Context {
	if _, ok := requestcontext.InternalTestIdentityFrom(ctx); ok {
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
	id := ClientIdentityFrom(ctx)
	ctx = observability.WithClientSessionID(ctx, id.SessionID)
	if id.Email == "" && id.AccountID == "" {
		if apiKey != nil && apiKey.CredentialSubjectID != "" {
			return authSvc.ResolveAndStashKeyOwner(ctx, installation.ID, apiKey.CredentialSubjectID, id.DisplayName)
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
		"email_present", id.Email != "",
		"account_present", id.AccountID != "",
		"name_present", id.DisplayName != "",
	)
	return authSvc.ResolveAndStashUser(ctx, installation.ID, id.Email, id.AccountID, id.DisplayName)
}

func ResolveRoutingAssignment(ctx context.Context, authSvc *auth.Service, installation *auth.Installation) (context.Context, error) {
	if _, ok := requestcontext.InternalTestIdentityFrom(ctx); ok {
		return ctx, nil
	}
	if authSvc == nil || installation == nil || auth.RoutingPolicyFrom(ctx).Mode != auth.RoutingPolicyAssigned {
		return ctx, nil
	}
	identity := ClientIdentityFrom(ctx)
	routerUserID, _ := ctx.Value(auth.UserIDContextKey{}).(string)
	if routerUserID == "" && (identity.Email != "" || identity.AccountID != "") {
		return ctx, auth.ErrRoutingPolicyUnavailable
	}
	return authSvc.WithRoutingAssignment(ctx, installation.ID, routerUserID)
}

type ClaudeCodeMetadata = requestcontext.ClaudeCodeMetadata

func ParseClaudeCodeMetadata(raw string) ClaudeCodeMetadata {
	return requestcontext.ParseClaudeCodeMetadata(raw)
}

const MaxClientIdentifierLen = requestcontext.MaxClientIdentifierLen

func NormalizeClientIdentifier(s string) string {
	return requestcontext.NormalizeClientIdentifier(s)
}

func completionLogClientApp(id ClientIdentity) string {
	if id.ClientApp == "" {
		return ""
	}
	if id.Eval {
		return "eval"
	}
	switch id.ClientApp {
	case requestcontext.ClientAppClaudeCode:
		return requestcontext.ClientAppClaudeCode
	case requestcontext.ClientAppCodex:
		return requestcontext.ClientAppCodex
	case requestcontext.ClientAppCursor:
		return requestcontext.ClientAppCursor
	case requestcontext.ClientAppGeminiCLI:
		return requestcontext.ClientAppGeminiCLI
	case requestcontext.ClientAppOpencode:
		return requestcontext.ClientAppOpencode
	default:
		return "other"
	}
}

func supportsResponsesTerminalSurfaces(clientApp string) bool {
	return clientApp == requestcontext.ClientAppCodex || clientApp == requestcontext.ClientAppOpencode
}

func directivePrefix(clientApp string) string {
	if clientApp == requestcontext.ClientAppCodex {
		return "$"
	}
	return "/"
}
