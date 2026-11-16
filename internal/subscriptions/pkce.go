package subscriptions

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

const pkceVerifierBytes = 32

type PKCEChallenge struct {
	Verifier  string
	Challenge string
}

func NewPKCEChallenge() (PKCEChallenge, error) {
	verifierBytes := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCEChallenge{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	digest := sha256.Sum256([]byte(verifier))
	return PKCEChallenge{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(digest[:]),
	}, nil
}

func VerifyPKCE(verifier, challenge string) error {
	if verifier == "" || challenge == "" {
		return errors.New("pkce verifier and challenge are required")
	}
	digest := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) != 1 {
		return errors.New("pkce challenge mismatch")
	}
	return nil
}
