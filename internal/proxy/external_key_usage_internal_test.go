package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

type recordedKeyUsage struct{ ids []string }

func (r *recordedKeyUsage) MarkExternalAPIKeyUsed(_ context.Context, id string) {
	r.ids = append(r.ids, id)
}

func TestMarkServedExternalKeyUsed(t *testing.T) {
	storedKey := BuildCredentialsMap([]*auth.ExternalAPIKey{{
		ID: "key-1", Provider: providers.ProviderOpenAI, Plaintext: []byte("sk-test"),
	}})[providers.ProviderOpenAI]
	cases := []struct {
		name      string
		creds     *Credentials
		servedErr error
		managed   *ManagedSubscriptionUsage
		want      []string
	}{
		{name: "served by stored key", creds: storedKey, want: []string{"key-1"}},
		{name: "failed request", creds: storedKey, servedErr: errors.New("upstream 500")},
		{name: "caller credential", creds: &Credentials{APIKey: []byte("sk-caller"), Source: credSourceClient}},
		{name: "deployment key"},
		{name: "served on managed subscription", creds: storedKey, managed: &ManagedSubscriptionUsage{Served: true, CredentialSource: credSourceSubscription}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordedKeyUsage{}
			svc := NewService(nil, map[string]providers.Client{}, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithExternalKeyUsageRecorder(recorder)
			ctx := context.Background()
			if tc.creds != nil {
				ctx = context.WithValue(ctx, CredentialsContextKey{}, tc.creds)
			}
			if tc.managed != nil {
				ctx = context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, tc.managed)
			}

			svc.markServedExternalKeyUsed(ctx, tc.servedErr)

			assert.Equal(t, tc.want, recorder.ids)
		})
	}
}
