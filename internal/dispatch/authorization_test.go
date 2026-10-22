package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func TestFundingAuthorizationSurvivesReplacedAttemptContext(t *testing.T) {
	for _, finalOAuth := range []bool{false, true} {
		name := "paid denied"
		if finalOAuth {
			name = "final OAuth allowed"
		}
		t.Run(name, func(t *testing.T) {
			upstream, alternative := &fakeUpstream{}, &fakeUpstream{}
			executor := newExecutor(t, map[string]providers.Client{primary.Provider: upstream, backup.Provider: alternative}, &recorder{})
			// A retryable underlying fault must not make a funding denial retryable.
			quotaUnknown := &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}
			ctx := dispatch.WithAuthorization(context.Background(), func(callCtx context.Context, target inference.Target) error {
				require.Equal(t, primary, target)
				if creds := requestcontext.CredentialsFromContext(callCtx); creds != nil && creds.OAuth {
					return nil
				}
				return quotaUnknown
			})
			invoke := attemptWith([]byte(`{"model":"kimi-k2.5"}`))
			attempts := 0
			result, err := executor.Run(ctx, inference.InvocationRequest{}, fakePlan{selected: primary, alternatives: []inference.Target{backup}}, dispatch.Transport{
				Prepare: func(context.Context, dispatch.Attempt) (context.Context, error) {
					return context.Background(), nil
				},
				Attempt: func(attemptCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
					attempts++
					if finalOAuth {
						attemptCtx = requestcontext.WithCredentials(attemptCtx, &requestcontext.Credentials{OAuth: true})
					}
					return invoke(attemptCtx, attempt, client)
				},
			})
			if finalOAuth {
				require.NoError(t, err)
				require.Equal(t, []string{primary.CatalogID}, upstream.models)
			} else {
				require.ErrorIs(t, err, dispatch.ErrAuthorizationDenied)
				require.True(t, errors.Is(err, quotaUnknown))
				require.Equal(t, string(dispatch.FailureReasonAuthorization), result.Summary.FallbackReason)
				require.Empty(t, upstream.models)
			}
			require.Empty(t, alternative.models)
			require.Equal(t, 1, attempts, "funding denial is terminal even when its cause is retryable")
		})
	}
}
