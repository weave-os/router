package auth

import "errors"

// subscriptionLoginAAD binds a sealed browser login to the subscriber that
// started it, so one subscriber's login token cannot finish under another.
const subscriptionLoginAAD = "subscription-login"

// SealSubscriptionLogin encrypts pending browser-login state (PKCE verifier,
// device code) for the control plane to hold until the login completes.
// CanSealSubscriptionLogins reports whether pending logins can be sealed; it
// needs a real keyset, since a plaintext encryptor would expose the PKCE verifier.
func (s *Service) CanSealSubscriptionLogins() bool {
	_, plaintext := s.encryptor.(NoOpEncryptor)
	return s.encryptor != nil && !plaintext
}

func (s *Service) SealSubscriptionLogin(subscriberID string, state []byte) ([]byte, error) {
	if !s.CanSealSubscriptionLogins() {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Encrypt(state, subscriberID, subscriptionLoginAAD)
}

// OpenSubscriptionLogin reverses SealSubscriptionLogin for the same subscriber.
func (s *Service) OpenSubscriptionLogin(subscriberID string, sealed []byte) ([]byte, error) {
	if !s.CanSealSubscriptionLogins() {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Decrypt(sealed, subscriberID, subscriptionLoginAAD)
}
