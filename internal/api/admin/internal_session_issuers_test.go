package admin_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type credentialSubjectLookupFunc func(context.Context, string, string) (*auth.CredentialSubject, error)

type sessionIssuerAPIKeyRepository struct {
	*fakeAPIKeyRepository
}

func (*sessionIssuerAPIKeyRepository) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return nil, nil, sql.ErrNoRows
}

func (lookup credentialSubjectLookupFunc) GetCredentialSubject(ctx context.Context, subjectID, installationID string) (*auth.CredentialSubject, error) {
	return lookup(ctx, subjectID, installationID)
}

func TestInternalRegisterSessionIssuerCanonicalizesSubjectUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonicalSubjectID := uuid.New().String()
	requestedSubjectID := strings.ToUpper(canonicalSubjectID)
	var lookedUpSubjectID string

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicJWK, err := (jose.JSONWebKey{Key: publicKey}).MarshalJSON()
	require.NoError(t, err)

	repository := &sessionIssuerAPIKeyRepository{fakeAPIKeyRepository: &fakeAPIKeyRepository{}}
	service := newAuthServiceForKeyTests(repository, nil).WithCredentialSubjectLookup(
		credentialSubjectLookupFunc(func(_ context.Context, subjectID, _ string) (*auth.CredentialSubject, error) {
			lookedUpSubjectID = subjectID
			parsedSubjectID, err := uuid.Parse(subjectID)
			if err != nil {
				return nil, err
			}
			return &auth.CredentialSubject{
				ID: parsedSubjectID.String(), ProjectionComplete: true, EnrollmentGeneration: 1, AccessEnabled: true,
			}, nil
		}),
	)
	engine := gin.New()
	engine.POST("/internal/session-issuers", admin.InternalRegisterSessionIssuerHandler(service))

	requestBody, err := json.Marshal(struct {
		InstallationID      string          `json:"installation_id"`
		CredentialSubjectID string          `json:"credential_subject_id"`
		PublicJWK           json.RawMessage `json:"public_jwk"`
	}{
		InstallationID: testInstallationID, CredentialSubjectID: requestedSubjectID, PublicJWK: publicJWK,
	})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/session-issuers", strings.NewReader(string(requestBody)))
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, canonicalSubjectID, lookedUpSubjectID)
}
