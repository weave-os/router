package openai

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"weave-os/router/internal/requestcontext"
)

func TestIncludedInferenceUsesFreshConnections(t *testing.T) {
	for _, includedOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "included_only"}[includedOnly], func(t *testing.T) {
			addresses := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				addresses <- r.RemoteAddr
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			ctx := context.Background()
			if includedOnly {
				ctx = requestcontext.WithIncludedSubscriptionOnly(ctx)
			}
			for range 2 {
				request, err := newInferenceRequest(ctx, server.URL, []byte(`{"model":"synthetic"}`))
				require.NoError(t, err)
				response, err := server.Client().Do(request)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
			}
			first, second := <-addresses, <-addresses
			if includedOnly {
				require.NotEqual(t, first, second)
			} else {
				require.Equal(t, first, second)
			}
		})
	}
}
