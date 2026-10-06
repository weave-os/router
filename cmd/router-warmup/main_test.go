package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestWarmupIncludesEveryModelWithSupportedEffort(t *testing.T) {
	plan := warmupPlan()
	if len(plan) != len(catalog.Listing()) {
		t.Fatal("warmup narrowed the catalog")
	}
	foundPro := false
	for _, prompt := range plan {
		levels := router.Lookup(prompt.Model).Reasoning().Levels
		if len(levels) > 0 && !slices.Contains(levels, prompt.ReasoningEffort) {
			t.Fatalf("unsupported effort for %s", prompt.Model)
		}
		if prompt.Model == "gpt-5.4-pro" {
			foundPro = true
			if prompt.ReasoningEffort != "medium" {
				t.Fatalf("pro warmup effort: %s", prompt.ReasoningEffort)
			}
		}
	}
	if !foundPro {
		t.Fatal("pro missing from broad warmup")
	}
}

func TestWarmupContinuesAfterProviderFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := executeWarmup(context.Background(), server.Client(), server.URL, "fixture-key", warmupPlan()[:2]); err == nil {
		t.Fatal("failure hidden")
	}
	if calls != 2 {
		t.Fatal("warmup stopped before remaining models")
	}
}
