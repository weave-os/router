package feedback

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("feedback: invalid token")

var ErrExpiredToken = errors.New("feedback: expired token")

type Claims struct {
	InstallationID string `json:"iid"`
	ExternalID     string `json:"eid"`
	RequestID      string `json:"rid"`
	RouterUserID   string `json:"uid,omitempty"`
	ExpiresAt      int64  `json:"exp"`
}

type Signer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewSigner(secret string, ttl time.Duration) *Signer {
	if secret == "" {
		return nil
	}
	return &Signer{secret: []byte(secret), ttl: ttl, now: time.Now}
}

func (s *Signer) Mint(installationID, externalID, requestID, routerUserID string) string {
	if s == nil {
		return ""
	}
	var exp int64
	if s.ttl > 0 {
		exp = s.now().Add(s.ttl).Unix()
	}
	claims := Claims{
		InstallationID: installationID,
		ExternalID:     externalID,
		RequestID:      requestID,
		RouterUserID:   routerUserID,
		ExpiresAt:      exp,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return ""
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body)
}

func (s *Signer) Verify(token string) (Claims, error) {
	if s == nil {
		return Claims{}, ErrInvalidToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return Claims{}, ErrInvalidToken
	}
	expected := s.sign(body)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt != 0 && s.now().Unix() >= claims.ExpiresAt {
		return Claims{}, ErrExpiredToken
	}
	return claims, nil
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
