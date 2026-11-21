package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptionlane"
)

func TestSummarizerAccess_DeclinesAKeyOwingAnUnpayableFee(t *testing.T) {
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{ID: "k", Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-byok")}})
	access := summarizerAccess{s: &Service{}, headers: http.Header{}}

	_, allowed := access.SummaryContext(ctx, providers.ProviderAnthropic)
	assert.True(t, allowed, "a funded turn summarizes on the key")

	unfunded, _ := billing.NewService(nil).WithByokFeeRate(0.05).TripSubscriptionLane(ctx, subscriptionlane.CreditsDepletedCovered)
	_, allowed = access.SummaryContext(unfunded, providers.ProviderAnthropic)
	assert.False(t, allowed, "an organization that cannot pay the BYOK fee must not be charged one for a summary")
}
