package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/websearch"
)

func TestKeenableWebSearchOptIn(t *testing.T) {
	t.Setenv("KEENABLE_API_KEY", "")
	for _, value := range []string{"", "false", "TRUE"} {
		t.Setenv("ROUTER_KEENABLE_WEB_SEARCH", value)
		require.Nil(t, keenableWebSearch(slog.Default()), "flag value %q", value)
	}

	t.Setenv("ROUTER_KEENABLE_WEB_SEARCH", "true")
	keyless := keenableWebSearch(slog.Default())
	require.NotNil(t, keyless)
}

func TestKeenableWebSearchUsesConfiguredBaseURLAndKey(t *testing.T) {
	for _, key := range []string{"", "synthetic-search-key"} {
		authHeader := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader <- r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"results":[]}`)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("ROUTER_KEENABLE_WEB_SEARCH", "true")
		t.Setenv("KEENABLE_BASE_URL", srv.URL)
		t.Setenv("KEENABLE_API_KEY", key)

		executor := keenableWebSearch(slog.Default())
		require.NotNil(t, executor)
		_, err := executor.Search(t.Context(), websearch.Query{Text: "documentation"})
		require.NoError(t, err)
		want := ""
		if key != "" {
			want = "Bearer " + key
		}
		select {
		case got := <-authHeader:
			require.Equal(t, want, got)
		default:
			t.Fatal("search did not reach the configured base URL")
		}
	}
}
