package auth

import (
	"context"
)

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
