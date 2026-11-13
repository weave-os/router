package auth_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestGatewayAnalyticsCredentialScopeIsIndependentOfRouting(t *testing.T) {
	for _, test := range []struct {
		name, token     string
		scope           auth.APIKeyScope
		lookupErr, want error
	}{
		{"analytics", "ra_test", auth.ScopeAnalyticsRead, nil, nil},
		{"routing prefix", "rk_test", auth.ScopeAnalyticsRead, nil, auth.ErrInvalidPrefix},
		{"routing scope", "ra_test", auth.ScopeRouting, nil, auth.ErrWrongKeyScope},
		{"legacy scope", "ra_test", "", nil, auth.ErrWrongKeyScope},
		{"revoked", "ra_test", auth.ScopeAnalyticsRead, sql.ErrNoRows, auth.ErrInvalidToken},
		{"storage unavailable", "ra_test", auth.ScopeAnalyticsRead, context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := auth.RoutingCredentialVerifier{Keys: gatewayCredentialRepo{key: &auth.APIKey{Scope: test.scope}, failure: test.lookupErr}}
			err := verifier.VerifyAnalyticsCredential(context.Background(), test.token)
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
		})
	}
}
