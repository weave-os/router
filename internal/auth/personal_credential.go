package auth

import "context"

// WithPersonalCredentialIssuer makes routing-key issuance and rotation subject-owned, mirroring hosted account-owned keys.
func (s *Service) WithPersonalCredentialIssuer(issuer PersonalCredentialIssuer) *Service {
	s.personalCredentials = issuer
	return s
}

func newAPIKeyParams(installationID string, scope APIKeyScope, name, createdBy *string) (CreateAPIKeyParams, string) {
	rawToken := GenerateID(scope.TokenPrefix())
	keyHash, keyPrefix, keySuffix := APITokenFingerprint(rawToken)
	return CreateAPIKeyParams{
		InstallationID: installationID,
		ExternalID:     GenerateID("kid"),
		Name:           name,
		KeyPrefix:      keyPrefix,
		KeyHash:        keyHash,
		KeySuffix:      keySuffix,
		Scope:          scope,
		CreatedBy:      createdBy,
	}, rawToken
}

func (s *Service) rotatePersonalAPIKey(ctx context.Context, target *APIKey, createdBy *string) (*APIKey, string, error) {
	if s.personalCredentials == nil {
		return nil, "", ErrPersonalCredentialRequired
	}
	params, rawToken := newAPIKeyParams(target.InstallationID, target.Scope, target.Name, createdBy)
	key, err := s.personalCredentials.RotateProjected(ctx, target.CredentialSubjectID, target.ID, params)
	if err != nil {
		return nil, "", err
	}
	s.invalidateInstallation(target.InstallationID)
	return key, rawToken, nil
}
