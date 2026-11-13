package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestRoutingTokenPrecedence(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-Weave-Router-Key", " dedicated ")
	headers.Set("Authorization", "bEaReR bearer")
	headers.Set("x-api-key", "fallback")
	assert.Equal(t, "dedicated", auth.RoutingTokenFromHeaders(headers))
	headers.Del("X-Weave-Router-Key")
	assert.Equal(t, "bearer", auth.RoutingTokenFromHeaders(headers))
	headers.Set("Authorization", "Bearer ")
	assert.Equal(t, "fallback", auth.RoutingTokenFromHeaders(headers))
	headers.Set("Authorization", "Basic other")
	assert.Equal(t, "fallback", auth.RoutingTokenFromHeaders(headers))
}

type gatewayCredentialRepo struct {
	auth.APIKeyRepository
	key     *auth.APIKey
	failure error
}

func (r gatewayCredentialRepo) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return r.key, &auth.Installation{ID: "installation"}, r.failure
}

func TestGatewayCredentialVerificationDoesNotLoadSecretsOrCaches(t *testing.T) {
	for _, test := range []struct {
		name    string
		scope   auth.APIKeyScope
		failure error
		want    error
	}{
		{"routing", auth.ScopeRouting, nil, nil},
		{"analytics", auth.ScopeAnalyticsRead, nil, auth.ErrWrongKeyScope},
		{"revoked", auth.ScopeRouting, sql.ErrNoRows, auth.ErrInvalidToken},
		{"database unavailable", auth.ScopeRouting, context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := &auth.APIKey{ID: "key", InstallationID: "installation", Scope: test.scope}
			svc := auth.RoutingCredentialVerifier{Keys: gatewayCredentialRepo{key: key, failure: test.failure}}
			installation, credential, err := svc.VerifyRoutingCredential(context.Background(), "rk_test")
			if test.want != nil {
				require.True(t, errors.Is(err, test.want))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "installation", installation.ID)
			assert.Equal(t, "key", credential.ID)
		})
	}
}
