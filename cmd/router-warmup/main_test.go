package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestWarmupIncludesEveryModelWithSupportedEffort(t *testing.T) {
	plan := warmupPlan()
	catalogModels := catalog.Listing()
	if len(plan) != len(catalogModels) {
		t.Fatal("warmup narrowed the catalog")
	}
	for _, model := range catalogModels {
		if !slices.ContainsFunc(plan, func(prompt warmupRequest) bool { return prompt.Model == model.Model }) {
			t.Fatalf("warmup missing %s", model.Model)
		}
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
		if prompt.ReasoningEffort != "" && prompt.ReasoningEffort != "none" && prompt.MaxCompletionTokens != warmupReasoningCompletionTokens {
			t.Fatalf("reasoning warmup budget for %s: %d", prompt.Model, prompt.MaxCompletionTokens)
		}
	}
	if !foundPro {
		t.Fatal("pro missing from broad warmup")
	}
}

func TestWarmupContinuesAfterProviderFailure(t *testing.T) {
	var stateMu sync.Mutex
	warmupCalls := 0
	clearCalls := 0
	warmupSession := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody := mustReadBody(t, r)
		stateMu.Lock()
		defer stateMu.Unlock()
		if strings.Contains(string(requestBody), "/unforce-model") {
			clearCalls++
			if r.Header.Get("Session-Id") != warmupSession {
				t.Errorf("clear session %q does not match warmup session %q", r.Header.Get("Session-Id"), warmupSession)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		warmupCalls++
		if warmupSession == "" {
			warmupSession = r.Header.Get("Session-Id")
		} else if r.Header.Get("Session-Id") != warmupSession {
			t.Errorf("warmup session changed from %q to %q", warmupSession, r.Header.Get("Session-Id"))
		}
		if warmupCalls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := executeWarmup(context.Background(), server.Client(), server.URL, "fixture-key", warmupPlan()[:2]); err == nil {
		t.Fatal("failure hidden")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if warmupCalls != 2 || clearCalls != 1 {
		t.Fatal("warmup stopped before remaining models")
	}
}

func mustReadBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
