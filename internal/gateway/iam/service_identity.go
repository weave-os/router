package iam

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/api/idtoken"
)

type serviceIdentityIssuer string

const (
	googleIdentityIssuer       serviceIdentityIssuer = "https://accounts.google.com"
	googleLegacyIdentityIssuer serviceIdentityIssuer = "accounts.google.com"
)

// ServiceIdentityVerifier restricts discovery to explicitly trusted workloads.
type ServiceIdentityVerifier struct {
	validator *idtoken.Validator
	audience  string
	subjects  map[string]struct{}
}

// NewServiceIdentityVerifier uses Google's signing keys, expiry and audience checks.
func NewServiceIdentityVerifier(ctx context.Context, audience string, subjects []string) (*ServiceIdentityVerifier, error) {
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("discovery audience is required")
	}
	allowed := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject != "" {
			allowed[subject] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("discovery service subjects are required")
	}
	validator, err := idtoken.NewValidator(ctx)
	if err != nil {
		return nil, err
	}
	return &ServiceIdentityVerifier{validator: validator, audience: audience, subjects: allowed}, nil
}

// VerifyServiceIdentity accepts only Google-issued tokens for an allowed subject.
func (v *ServiceIdentityVerifier) VerifyServiceIdentity(ctx context.Context, token string) error {
	payload, err := v.validator.Validate(ctx, token, v.audience)
	if err != nil {
		// Validator errors can quote attacker-controlled JWT fields. Keep tokens
		// and their contents out of the gateway's rejection log.
		return errors.New("invalid discovery service identity token")
	}
	issuer := serviceIdentityIssuer(payload.Issuer)
	if issuer != googleIdentityIssuer && issuer != googleLegacyIdentityIssuer {
		return errors.New("discovery service identity issuer is not allowed")
	}
	if _, allowed := v.subjects[payload.Subject]; !allowed {
		return errors.New("discovery service identity subject is not allowed")
	}
	return nil
}
