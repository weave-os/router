package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

func (s *Service) ResolveAndStashUnidentifiedKeyUser(ctx context.Context, installationID, apiKeyID, displayName string) context.Context {
	log := observability.FromContext(ctx)
	if s.users == nil || installationID == "" {
		return ctx
	}
	identityKey := "unidentified-key:" + apiKeyID
	if cached, ok := s.userCache.Get(installationID, identityKey); ok {
		return s.withUserSettings(ctx, installationID, cached)
	}
	var optionalName *string
	if displayName != "" {
		optionalName = &displayName
	}
	user, err := s.users.UpsertByUnidentifiedAPIKey(ctx, UpsertUserByUnidentifiedAPIKeyParams{
		InstallationID:       installationID,
		UnidentifiedAPIKeyID: apiKeyID,
		DisplayName:          optionalName,
	})
	if err != nil {
		log.Warn("Failed to resolve router user for unidentified API key traffic", "installation_id", installationID, "err", err)
		return ctx
	}
	s.userCache.Set(installationID, identityKey, user.ID)
	log.Debug("ResolveAndStashUnidentifiedKeyUser upsert ok", "installation_id", installationID, "user_id", user.ID)
	return s.withUserSettings(ctx, installationID, user.ID)
}
