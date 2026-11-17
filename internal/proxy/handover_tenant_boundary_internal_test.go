package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/translate"
)

type credentialRecordingSummarizer struct {
	calls int
	creds *Credentials
}

func (r *credentialRecordingSummarizer) Summarize(ctx context.Context, _ *translate.RequestEnvelope, _ router.Request) (string, handover.Usage, error) {
	r.calls++
	r.creds, _ = ctx.Value(CredentialsContextKey{}).(*Credentials)
	return "Earlier: step 1 done.", handover.Usage{}, nil
}

func (*credentialRecordingSummarizer) Provider() string { return providers.ProviderAnthropic }

func TestRunCompactionHandover_SummarizerNeverCrossesTenantCredentialBoundary(t *testing.T) {
	t.Parallel()

	byokCtx := func(provider string) context.Context {
		return context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{
			Provider:  provider,
			Plaintext: []byte("synthetic-byok-" + provider),
		}})
	}
	cases := []struct {
		name             string
		ctx              context.Context
		wantCalls        int
		wantFallback     bool
		wantCredsPresent bool
	}{
		{name: "byok_for_other_provider_skips_summarizer", ctx: byokCtx(providers.ProviderOpenAI), wantFallback: true},
		{name: "byok_for_summarizer_provider_funds_it", ctx: byokCtx(providers.ProviderAnthropic), wantCalls: 1, wantCredsPresent: true},
		{name: "deployment_credentials_run_on_deployment_key", ctx: context.Background(), wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, err := translate.ParseAnthropic([]byte(sampleConversation))
			require.NoError(t, err)
			sz := &credentialRecordingSummarizer{}
			svc := NewService(nil, nil, nil, nil, false, "", "", nil).WithCompactionHandoverSummarizer(sz)

			out := svc.runCompactionHandover(tc.ctx, env, http.Header{}, "compat-model", router.Request{})

			assert.True(t, out.Invoked)
			assert.Equal(t, tc.wantFallback, out.FallbackToFullHistory)
			assert.Equal(t, tc.wantCalls, sz.calls)
			assert.Equal(t, tc.wantCredsPresent, sz.creds != nil)
		})
	}
}
