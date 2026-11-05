package auth

import "errors"

// subscriptionLoginAAD binds a sealed browser login to the subscriber that
// started it, so one subscriber's login token cannot finish under another.
const subscriptionLoginAAD = "subscription-login"

// SealSubscriptionLogin encrypts pending browser-login state (PKCE verifier,
// device code) for the control plane to hold until the login completes.
func (s *Service) SealSubscriptionLogin(subscriberID string, state []byte) ([]byte, error) {
	if _, plaintext := s.encryptor.(NoOpEncryptor); s.encryptor == nil || plaintext {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Encrypt(state, subscriberID, subscriptionLoginAAD)
}

// OpenSubscriptionLogin reverses SealSubscriptionLogin for the same subscriber.
func (s *Service) OpenSubscriptionLogin(subscriberID string, sealed []byte) ([]byte, error) {
	if _, plaintext := s.encryptor.(NoOpEncryptor); s.encryptor == nil || plaintext {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Decrypt(sealed, subscriberID, subscriptionLoginAAD)
}
