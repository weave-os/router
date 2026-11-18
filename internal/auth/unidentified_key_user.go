package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

func (s *Service) ResolveAndStashUnidentifiedKeyUser(ctx context.Context, installationID, sharedKeyID string) context.Context {
	return s.resolveAndStashScopedUser(ctx, installationID, "unidentified-key:"+sharedKeyID, func() (*User, error) {
		return s.users.UpsertByUnidentifiedKey(ctx, UpsertUserByUnidentifiedKeyParams{
			InstallationID: installationID,
			SharedKeyID:    sharedKeyID,
		})
	})
}

func (s *Service) resolveAndStashScopedUser(ctx context.Context, installationID, cacheKey string, upsert func() (*User, error)) context.Context {
	if s.users == nil || installationID == "" {
		return ctx
	}
	if cached, ok := s.userCache.Get(installationID, cacheKey); ok {
		return s.withUserSettings(ctx, installationID, cached)
	}
	user, err := upsert()
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to resolve router user without client identity", "installation_id", installationID, "err", err)
		return ctx
	}
	s.userCache.Set(installationID, cacheKey, user.ID)
	return s.withUserSettings(ctx, installationID, user.ID)
}
