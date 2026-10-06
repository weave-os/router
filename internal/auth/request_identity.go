package auth

import (
	"context"
)

// SubscriptionOwnerForRequestUncached resolves the caller against the live
// projection, ignoring the cache. Managing a subscription account is rare and
// destructive, so it reads the projection Weave has now rather than the one it
// had up to a TTL ago, and a withdrawn identity stops naming its former owner
// immediately.
func (s *Service) SubscriptionOwnerForRequestUncached(ctx context.Context, key *APIKey) (SubscriptionOwner, error) {
	if key == nil || key.CredentialSubjectID == "" || s.credentialSubjects == nil {
		return SubscriptionOwner{}, ErrPersonalCredentialRequired
	}
	subject, err := s.credentialSubjects.GetCredentialSubject(ctx, key.CredentialSubjectID, key.InstallationID)
	if err != nil {
		return SubscriptionOwner{}, err
	}
	if err := ValidateCredentialSubject(*key, subject); err != nil {
		return SubscriptionOwner{}, err
	}
	return SubscriptionOwnerForKey(key), nil
}
