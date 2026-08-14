//go:build smoke

package smoke

import (
	"net/http"
	"strings"
	"testing"
)

// TestBasic exercises the core request path: the force-model command surface,
// a non-stream turn, and a streamed turn — all pinned to the cheap model and
// served by Anthropic.
func TestBasic(t *testing.T) {
	t.Run("force-model command turn", func(t *testing.T) {
		// A /force-model command returns a synthetic 200 acknowledgement handled
		// entirely inside the router (no upstream call).
		body := forceModelBody(t, "smoke-basic-cmd", cfg.PinModel)
		r := call(t, body)
		if r.status != http.StatusOK {
			t.Fatalf("force-model command: want 200, got %d; body: %s", r.status, truncate(r.body, 400))
		}
		if r.message == nil || r.message.Type != "message" {
			t.Fatalf("force-model command: want a message body, got: %s", truncate(r.body, 400))
		}
	})

	t.Run("non-stream turn served by pinned model", func(t *testing.T) {
		body := newRequest("smoke-basic-nonstream").tokens(64).
			text("Reply with exactly the word: ok").build(t)
		r := call(t, body)
		requireOKMessage(t, r)

		if r.message.Usage.InputTokens <= 0 {
			t.Errorf("want input_tokens > 0, got %d", r.message.Usage.InputTokens)
		}
		if r.message.Usage.OutputTokens <= 0 {
			t.Errorf("want output_tokens > 0, got %d", r.message.Usage.OutputTokens)
		}
		if len(r.message.Content) == 0 {
			t.Errorf("want non-empty content")
		}
		assertServedByPin(t, r)
	})

	t.Run("streamed turn is well-ordered", func(t *testing.T) {
		body := newRequest("smoke-basic-stream").tokens(64).streaming().
			text("Reply with exactly the word: ok").build(t)
		r := call(t, body)
		if r.status != http.StatusOK {
			t.Fatalf("stream: want 200, got %d; body: %s", r.status, truncate(r.body, 400))
		}
		assertStreamWellFormed(t, r)
		if r.message == nil || r.message.Usage.InputTokens <= 0 {
			t.Errorf("stream: want input_tokens > 0 in message_start usage")
		}
		assertServedByPin(t, r)
	})
}

// TestForceHeaderRejections covers the two force-header refusals that resolve
// before any upstream call, so they need no cassette and run in replay-only CI.
//
// The success path (a label that IS in the live roster serving from that
// cluster's arms) is deliberately absent: it needs the HMM sidecar container,
// which the smoke stack doesn't boot, and the MITM proxy intercepts only
// outbound HTTPS provider calls — not the router's plain-HTTP sidecar hop — so
// there is nothing to record it against. Verify that path against a real
// sidecar (`make up-hmm`).
func TestForceHeaderRejections(t *testing.T) {
	t.Run("force-cluster on the default cluster strategy is refused", func(t *testing.T) {
		// The default strategy scores anonymous centroids, so there is no named
		// cluster to constrain to. Silently serving would look like the force took.
		body := newRequest("smoke-force-cluster-unsupported").tokens(64).
			text("Reply with exactly the word: ok").build(t)
		r := callModelWithHeaders(t, body, cfg.PinModel,
			map[string]string{forceClusterHeader: "maximum"})

		if r.status != http.StatusBadRequest {
			t.Fatalf("want 400, got %d; body: %s", r.status, truncate(r.body, 600))
		}
		if !strings.Contains(string(r.body), "invalid_request_error") {
			t.Errorf("want an invalid_request_error envelope, got: %s", truncate(r.body, 600))
		}
	})

	t.Run("force-model naming no catalog model is refused", func(t *testing.T) {
		body := newRequest("smoke-force-model-unknown").tokens(64).
			text("Reply with exactly the word: ok").build(t)
		r := callModel(t, body, "totally-not-a-model")

		if r.status != http.StatusBadRequest {
			t.Fatalf("want 400, got %d; body: %s", r.status, truncate(r.body, 600))
		}
		if !strings.Contains(string(r.body), "totally-not-a-model") {
			t.Errorf("want the unresolvable value quoted back, got: %s", truncate(r.body, 600))
		}
	})
}

// assertServedByPin checks the x-router-model / x-router-provider decision
// headers name the pinned model on Anthropic.
func assertServedByPin(t *testing.T, r response) {
	t.Helper()
	assertServedByModel(t, r, cfg.PinModel, "anthropic")
}

// assertServedByModel is assertServedByPin generalized to an arbitrary
// model/provider pair, for scenarios that pin something other than the
// suite-wide default (e.g. a gpt-5.x model on the direct OpenAI provider).
func assertServedByModel(t *testing.T, r response, wantModel, wantProvider string) {
	t.Helper()
	gotModel := r.headers.Get(headerRouterModel)
	if gotModel == "" {
		t.Errorf("missing %s header", headerRouterModel)
	} else if !strings.Contains(gotModel, wantModel) && gotModel != wantModel {
		t.Errorf("want %s header = %q (pinned), got %q", headerRouterModel, wantModel, gotModel)
	}
	if gotProvider := r.headers.Get(headerRouterProvider); gotProvider == "" {
		t.Errorf("missing %s header", headerRouterProvider)
	} else if gotProvider != wantProvider {
		t.Errorf("want %s = %s, got %q", headerRouterProvider, wantProvider, gotProvider)
	}
}

// Response-header names; kept in sync with internal/proxy/headers.go.
const (
	headerRouterModel    = "x-router-model"
	headerRouterProvider = "x-router-provider"
	headerRouterDecision = "x-router-decision"
)
