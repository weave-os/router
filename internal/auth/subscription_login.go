package auth

import "errors"

const subscriptionLoginAAD = "subscription-login"

func (s *Service) SealSubscriptionLogin(subscriberID string, state []byte) ([]byte, error) {
	if _, plaintext := s.encryptor.(NoOpEncryptor); s.encryptor == nil || plaintext {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Encrypt(state, subscriberID, subscriptionLoginAAD)
}

func (s *Service) OpenSubscriptionLogin(subscriberID string, sealed []byte) ([]byte, error) {
	if _, plaintext := s.encryptor.(NoOpEncryptor); s.encryptor == nil || plaintext {
		return nil, errors.New("subscription login sealing is not configured")
	}
	return s.encryptor.Decrypt(sealed, subscriberID, subscriptionLoginAAD)
}
