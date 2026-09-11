package dispatch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

// NativeReason is recorded on the router.Decision handed to the provider for a
// preserved native original-model request.
const NativeReason = "original_model_fallback"

// Native describes one streaming pass of the caller's own native request to
// the plan target: the prepared body and headers are forwarded with
// PreserveNative set, and the response is written straight to Writer. Model is
// the original model spelling the caller asked for; it must name the plan
// target's catalog or upstream ID or the attempt fails before any upstream
// I/O. When the body carries no top-level model (Gemini), the inbound request
// path is checked instead.
type Native struct {
	Prepared providers.PreparedRequest
	Request  *http.Request
	Writer   http.ResponseWriter
	Model    string
}

// Transport adapts n into an executor Transport that performs exactly one
// provider call per attempt and never rewrites the target.
func (n Native) Transport() Transport {
	return Transport{Attempt: func(ctx context.Context, attempt Attempt, client providers.Client) error {
		if n.Request == nil || n.Writer == nil {
			return fmt.Errorf("dispatch: native transport requires an inbound request and writer")
		}
		if err := n.validateTarget(attempt.Target); err != nil {
			return err
		}
		prep := n.Prepared
		prep.PreserveNative = true
		decision := router.Decision{
			Provider: attempt.Target.Provider,
			Model:    attempt.Target.CatalogID,
			Effort:   attempt.Target.Effort,
			Reason:   NativeReason,
		}
		return client.Proxy(ctx, decision, prep, n.Writer, n.Request)
	}}
}

// validateTarget checks every model identity the caller supplied: the declared
// original model, the body's top-level model, and a Gemini-style path model.
// Any of them naming a different model is a target mismatch.
func (n Native) validateTarget(target inference.Target) error {
	if err := ValidateWireModel(n.Model, target); err != nil {
		return err
	}
	if err := ValidatePreparedTarget(n.Prepared, target); err != nil {
		return err
	}
	if gjson.GetBytes(n.Prepared.Body, "model").Exists() {
		return nil
	}
	pathModel, found := pathModel(n.Request.URL)
	if !found {
		return nil
	}
	return ValidateWireModel(pathModel, target)
}

// pathModel extracts the model segment from a Gemini-style
// /v1beta/models/{model}:{action} path.
func pathModel(inbound *url.URL) (model string, found bool) {
	if inbound == nil {
		return "", false
	}
	const marker = "/models/"
	index := strings.LastIndex(inbound.Path, marker)
	if index < 0 {
		return "", false
	}
	segment := inbound.Path[index+len(marker):]
	if slash := strings.IndexByte(segment, '/'); slash >= 0 {
		segment = segment[:slash]
	}
	if colon := strings.IndexByte(segment, ':'); colon >= 0 {
		segment = segment[:colon]
	}
	if segment == "" {
		return "", false
	}
	return segment, true
}
