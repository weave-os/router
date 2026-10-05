package auth

import (
	"context"
)

// RequestIdentityRepository resolves the credential subject Weave projected
// for an email inside one installation. The router stores no accounts, so this
// projection is the only way an inbound request email becomes a person.
type RequestIdentityRepository interface {
	GetSubscriberForEmail(ctx context.Context, installationID, email string) (string, error)
}

// WithRequestIdentities retains composition-root compatibility for attribution
// projections. Email projections grant no personal subscription access.
func (s *Service) WithRequestIdentities(_ RequestIdentityRepository) *Service { return s }

// SubscriptionOwnerForRequest binds personal capacity to the authenticated key
// subject. Client-asserted email is attribution only and cannot grant ownership.
// Live candidate SQL revalidates subject eligibility and installation membership.
func (s *Service) SubscriptionOwnerForRequest(_ context.Context, key *APIKey, _ string) (SubscriptionOwner, error) {
	return SubscriptionOwnerForKey(key), nil
}

// SubscriptionOwnerForRequestUncached resolves the caller against the live
// projection, ignoring the cache. Managing a subscription account is rare and
// destructive, so it reads the projection Weave has now rather than the one it
// had up to a TTL ago, and a withdrawn identity stops naming its former owner
// immediately.
func (s *Service) SubscriptionOwnerForRequestUncached(ctx context.Context, key *APIKey, _ string) (SubscriptionOwner, error) {
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
