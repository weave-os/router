package cluster_test

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
)

// fixedEmbedder returns precomputed unit vectors in rotation, so routing is
// deterministic and needs no ONNX runtime.
type fixedEmbedder struct {
	id   string
	vecs [][]float32
	next int
}

func (f *fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	v := f.vecs[f.next%len(f.vecs)]
	f.next++
	return v, nil
}

func (f *fixedEmbedder) ID() string { return f.id }
func (f *fixedEmbedder) Dim() int   { return len(f.vecs[0]) }

func unitVectors(n, dim int) [][]float32 {
	rng := rand.New(rand.NewSource(1))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, dim)
		var norm float64
		for j := range v {
			v[j] = float32(rng.NormFloat64())
			norm += float64(v[j]) * float64(v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(norm))
		}
		out[i] = v
	}
	return out
}

// BenchmarkHotPathClusterRoute measures one routing decision through the
// shipped default bundle: prompt truncation, cluster scoring, eligibility
// under the request's provider set and the catalog lookup of the winner.
func BenchmarkHotPathClusterRoute(b *testing.B) {
	version, err := cluster.ResolveVersion(cluster.LatestVersion)
	if err != nil {
		b.Fatal(err)
	}
	bundle, err := cluster.LoadBundle(version)
	if err != nil {
		b.Fatal(err)
	}
	available := map[string]struct{}{
		providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}, providers.ProviderGoogle: {},
		providers.ProviderFireworks: {}, providers.ProviderDeepInfra: {}, providers.ProviderBedrock: {},
		providers.ProviderOpenRouter: {},
	}
	embed := &fixedEmbedder{id: bundle.EmbedderID(), vecs: unitVectors(8, bundle.EmbedDim())}
	scorer, err := cluster.NewScorer(bundle, cluster.DefaultConfig(), embed, available)
	if err != nil {
		b.Fatal(err)
	}
	req := router.Request{
		PromptText:       strings.Repeat("Refactor the request handler and add a regression test. ", 40),
		RequestedModel:   "claude-sonnet-4-5",
		HasTools:         true,
		EnabledProviders: available,
	}
	ctx := observability.WithLogger(context.Background(), slog.New(slog.DiscardHandler))
	decide := func() (router.Decision, error) {
		d, err := scorer.Route(ctx, req)
		if err != nil {
			return d, err
		}
		if _, ok := catalog.ByID(d.Model); !ok {
			return d, &unknownModelError{model: d.Model}
		}
		return d, nil
	}
	for range embed.vecs {
		d, err := decide()
		if err != nil {
			b.Fatal(err)
		}
		if _, ok := available[d.Provider]; !ok {
			b.Fatalf("decision provider %q is outside the enabled set", d.Provider)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decide(); err != nil {
			b.Fatal(err)
		}
	}
}

type unknownModelError struct{ model string }

func (e *unknownModelError) Error() string {
	return "routed to model missing from catalog: " + strconv.Quote(e.model)
}
