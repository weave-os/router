package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

const AdminSessionCookieName = "router_admin_session"

const DefaultAdminSessionTTL = 7 * 24 * time.Hour

var ErrAdminSessionInvalid = errors.New("admin session invalid")

var ErrAdminPasswordNotConfigured = errors.New("admin password not configured")

var ErrAdminLoginRateLimited = errors.New("admin login rate limited")

const (
	adminLoginMaxFailures = 5
	adminLoginFailureTTL  = 5 * time.Minute
)

const adminSessionLabel = "router-admin-session-v1"

const AdminInstallationExternalID = "__router_admin__"

const AdminInstallationName = "Dashboard"

type adminClaims struct {
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type AdminPrincipal struct {
	Subject   string
	ExpiresAt time.Time
}

func (s *Service) WithAdminPassword(password string) *Service {
	s.adminPassword = password
	if password == "" {
		s.adminSessionKey = nil
		return s
	}
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte(adminSessionLabel))
	s.adminSessionKey = mac.Sum(nil)
	s.adminLoginMu.Lock()
	if s.adminLoginFailures == nil {
		s.adminLoginFailures = expirable.NewLRU[string, int](1024, nil, adminLoginFailureTTL)
	}
	s.adminLoginMu.Unlock()
	return s
}

func (s *Service) AdminLoginEnabled() bool {
	return s.adminPassword != "" && len(s.adminSessionKey) == sha256.Size
}

func (s *Service) VerifyAdminPassword(password string) error {
	if !s.AdminLoginEnabled() {
		return ErrAdminPasswordNotConfigured
	}
	if subtle.ConstantTimeCompare([]byte(s.adminPassword), []byte(password)) == 1 {
		return nil
	}
	return ErrAdminSessionInvalid
}

func (s *Service) VerifyAdminPasswordFromIP(remoteIP, password string) error {
	if !s.AdminLoginEnabled() {
		return ErrAdminPasswordNotConfigured
	}
	if s.adminLoginFailures != nil && remoteIP != "" {
		if count, ok := s.adminLoginFailures.Get(remoteIP); ok && count >= adminLoginMaxFailures {
			return ErrAdminLoginRateLimited
		}
	}
	if err := s.VerifyAdminPassword(password); err != nil {
		if s.adminLoginFailures != nil && remoteIP != "" && errors.Is(err, ErrAdminSessionInvalid) {
			count, _ := s.adminLoginFailures.Get(remoteIP)
			s.adminLoginFailures.Add(remoteIP, count+1)
		}
		return err
	}
	if s.adminLoginFailures != nil && remoteIP != "" {
		s.adminLoginFailures.Remove(remoteIP)
	}
	return nil
}

func (s *Service) IssueAdminSession() (token string, expiresAt time.Time, err error) {
	if !s.AdminLoginEnabled() {
		return "", time.Time{}, ErrAdminPasswordNotConfigured
	}
	now := s.now()
	expiresAt = now.Add(DefaultAdminSessionTTL)
	claims := adminClaims{
		Subject:   "admin",
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal admin claims: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.adminSessionKey)
	mac.Write([]byte(encodedPayload))
	encodedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + encodedSig, expiresAt, nil
}

func (s *Service) EnsureAdminInstallation(ctx context.Context) (*Installation, error) {
	if inst, ok, err := s.findAdminInstallation(ctx); err != nil {
		return nil, err
	} else if ok {
		return inst, nil
	}
	created, createErr := s.installations.Create(ctx, CreateInstallationParams{
		ExternalID: AdminInstallationExternalID,
		Name:       AdminInstallationName,
	})
	if createErr == nil {
		return created, nil
	}
	if inst, ok, err := s.findAdminInstallation(ctx); err == nil && ok {
		return inst, nil
	}
	return nil, createErr
}

func (s *Service) findAdminInstallation(ctx context.Context) (*Installation, bool, error) {
	existing, err := s.installations.ListForExternalID(ctx, AdminInstallationExternalID)
	if err != nil {
		return nil, false, err
	}
	for _, inst := range existing {
		if inst != nil && inst.DeletedAt == nil {
			return inst, true, nil
		}
	}
	return nil, false, nil
}

func (s *Service) VerifyAdminSession(token string) (*AdminPrincipal, error) {
	if !s.AdminLoginEnabled() {
		return nil, ErrAdminPasswordNotConfigured
	}
	encodedPayload, encodedSig, ok := strings.Cut(token, ".")
	if !ok || encodedPayload == "" || encodedSig == "" {
		return nil, ErrAdminSessionInvalid
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil {
		return nil, ErrAdminSessionInvalid
	}
	mac := hmac.New(sha256.New, s.adminSessionKey)
	mac.Write([]byte(encodedPayload))
	wantSig := mac.Sum(nil)
	if !hmac.Equal(gotSig, wantSig) {
		return nil, ErrAdminSessionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, ErrAdminSessionInvalid
	}
	var claims adminClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrAdminSessionInvalid
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if !s.now().Before(expiresAt) {
		return nil, ErrAdminSessionInvalid
	}
	return &AdminPrincipal{Subject: claims.Subject, ExpiresAt: expiresAt}, nil
}
