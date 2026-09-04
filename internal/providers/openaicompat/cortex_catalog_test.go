package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
)

// cortex replays Snowflake Cortex's observed catalog responses: 415 without a
// content type, 400 without an entity, 400 without Accept, models otherwise.
func cortex(t *testing.T, hits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/cortex/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "HEAD,GET,OPTIONS")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"code":"391910","message":"Invalid input value. null"}`))
			return
		}
		if len(body) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"390400","message":"The request entity had the following errors: request entity required"}`))
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"390400","message":"Unsupported Accept header null is specified. Result set format must be JSON."}`))
			return
		}
		*hits++
		_, _ = w.Write([]byte(`{"models":["claude-4-sonnet","openai-gpt-5"]}`))
	}
}

func TestCortexSimOpenAI(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(cortex(t, &hits))
	defer srv.Close()
	models, err := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex").ListModels(context.Background())
	if err != nil || len(models) != 2 || hits != 1 {
		t.Fatalf("openai_gateway: models=%v err=%v hits=%d", models, err, hits)
	}
}

func TestCortexSimAnthropic(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(cortex(t, &hits))
	defer srv.Close()
	models, err := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex", anthropic.WithAuthScheme(anthropic.AuthBearer)).ListModels(context.Background())
	if err != nil || len(models) != 2 || hits != 1 {
		t.Fatalf("anthropic_gateway: models=%v err=%v hits=%d", models, err, hits)
	}
}
