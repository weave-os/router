package randomrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestUniformDrawBoundaries(t *testing.T) {
	for index, model := range Models() {
		t.Run(model, func(t *testing.T) {
			calls := 0
			policy := New(func(size int) (int, error) { require.Equal(t, 3, size); calls++; return index, nil })
			for range 2 {
				decision, err := policy.Route(WithRequest(context.Background(), &RequestState{Prepared: true, Config: Config{PolicySHA256: PolicySHA256}}), router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}})
				require.NoError(t, err)
				require.Equal(t, model, decision.Model)
				require.InDelta(t, float32(1.0/3.0), decision.Metadata.Propensity, 1e-8)
				require.Equal(t, PolicySHA256, decision.Metadata.PolicyArtifactSHA256)
			}
			require.Equal(t, 2, calls)
		})
	}
}

func TestMissingCandidateFailsBeforeDraw(t *testing.T) {
	policy := New(func(int) (int, error) { t.Fatal("incomplete roster reached random draw"); return 0, nil })
	for _, model := range Models() {
		_, err := policy.Route(WithRequest(context.Background(), &RequestState{Prepared: true, Config: Config{PolicySHA256: PolicySHA256}}), router.Request{ExcludedModels: map[string]struct{}{model: {}}})
		require.ErrorIs(t, err, ErrUnavailable)
	}
}

func TestInvalidDrawFailsClosed(t *testing.T) {
	for _, draw := range []Draw{func(int) (int, error) { return 3, nil }, func(int) (int, error) { return 0, errors.New("entropy unavailable") }} {
		_, err := New(draw).Route(WithRequest(context.Background(), &RequestState{Prepared: true, Config: Config{PolicySHA256: PolicySHA256}}), router.Request{})
		require.ErrorIs(t, err, ErrUnavailable)
	}
}

func TestStrictCandidateConstraints(t *testing.T) {
	for _, model := range Models() {
		for _, request := range []router.Request{
			{AllowedModels: map[string]struct{}{}},
			{EnabledProviders: map[string]struct{}{}},
			{GatewayProviders: map[string]struct{}{providers.ProviderOpenAI: {}}},
			{AutomaticExcludedModels: map[string]struct{}{model: {}}},
			{SafetyExcludedModels: map[string]struct{}{model: {}}},
		} {
			p := New(func(int) (int, error) { t.Fatal("ineligible roster reached draw"); return 0, nil })
			_, err := p.Route(WithRequest(context.Background(), &RequestState{Config: Config{PolicySHA256: PolicySHA256}}), request)
			require.ErrorIs(t, err, ErrUnavailable)
		}
	}
}
