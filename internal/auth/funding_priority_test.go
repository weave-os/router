package auth_test

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertExternalKeyFundingPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		existing  []*auth.ExternalAPIKey
		requested auth.FundingPriority
		want      auth.FundingPriority
	}{
		{name: "new key defaults to plan first", want: auth.FundingPriorityPlanFirst},
		{
			name:     "rotation keeps the replaced key's priority",
			existing: []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst}},
			want:     auth.FundingPriorityKeyFirst,
		},
		{
			name:     "another provider's priority is not inherited",
			existing: []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenAI, FundingPriority: auth.FundingPriorityKeyFirst}},
			want:     auth.FundingPriorityPlanFirst,
		},
		{
			name:      "explicit priority wins over the replaced key",
			existing:  []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst}},
			requested: auth.FundingPriorityPlanFirst,
			want:      auth.FundingPriorityPlanFirst,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingExternalKeyRepo{fakeExternalAPIKeyRepo: fakeExternalAPIKeyRepo{keys: tc.existing}}
			svc := makeServiceWithExternalKeys(t, repo)
			_, err := svc.UpsertExternalAPIKey(context.Background(), "install_synthetic", auth.UpsertExternalAPIKeyParams{
				Provider: providers.ProviderAnthropic, RawKey: "sk-ant-api03-synthetic", FundingPriority: tc.requested,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, repo.created.FundingPriority)
		})
	}
}
