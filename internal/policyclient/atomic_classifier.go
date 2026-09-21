package policyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"weave-os/router/internal/router"
)

const atomicClassifierMaxRequestBytes = 1_000_000
const atomicClassifierMaxResponseBytes = 16_384

// AtomicClassifier is the authenticated V3 Modal transport. Its URL and release
// pins are server configuration, never selected by an inference request.
type AtomicClassifier struct {
	endpoint      string
	bearer        string
	release       string
	releaseSHA256 string
	client        *http.Client
}

// NewAtomicClassifier forbids redirects so a service cannot move the credential
// or prompt to another origin. There is no policy fallback or implicit retry.
func NewAtomicClassifier(endpoint, bearer, release, releaseSHA256 string, client *http.Client) (*AtomicClassifier, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || len(bearer) < 32 || strings.ContainsAny(bearer, "\r\n") || release == "" || len(releaseSHA256) != 64 {
		return nil, fmt.Errorf("invalid atomic classifier endpoint, authentication or release")
	}
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	return &AtomicClassifier{endpoint: strings.TrimRight(endpoint, "/") + "/classify", bearer: bearer, release: release, releaseSHA256: releaseSHA256,
		client: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Classify sends only V3 inference inputs and validates every response's release.
func (c *AtomicClassifier) Classify(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return router.ClassifierPrediction{}, err
	}
	if len(payload) > atomicClassifierMaxRequestBytes {
		return router.ClassifierPrediction{}, fmt.Errorf("classifier request exceeds byte limit: %w", router.ErrClassifierInputTooLong)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.ClassifierPrediction{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return router.ClassifierPrediction{}, fmt.Errorf("classifier transport: %w: %w", err, router.ErrClassifierUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestEntityTooLarge {
		return router.ClassifierPrediction{}, router.ErrClassifierInputTooLong
	}
	if response.StatusCode != http.StatusOK {
		// Bodies can echo private inputs; retain status only.
		return router.ClassifierPrediction{}, fmt.Errorf("classifier HTTP %d: %w", response.StatusCode, router.ErrClassifierUnavailable)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, atomicClassifierMaxResponseBytes+1))
	if err != nil {
		return router.ClassifierPrediction{}, fmt.Errorf("read classifier response: %w: %w", err, router.ErrClassifierUnavailable)
	}
	if len(body) > atomicClassifierMaxResponseBytes {
		return router.ClassifierPrediction{}, fmt.Errorf("classifier response exceeds byte limit: %w", router.ErrClassifierUnavailable)
	}
	var classifierFacts struct {
		Release       string                         `json:"release"`
		ReleaseSHA256 string                         `json:"release_sha256"`
		Prediction    *router.ClassifierComplexity   `json:"prediction"`
		Probabilities []float64                      `json:"probabilities"`
		InputTokens   int                            `json:"input_tokens"`
		HistorySource router.ClassifierHistorySource `json:"history_source"`
	}
	if err := json.Unmarshal(body, &classifierFacts); err != nil {
		// Avoid echoing untrusted field values from a decoder error.
		return router.ClassifierPrediction{}, fmt.Errorf("decode classifier facts (%T): %w", err, router.ErrClassifierUnavailable)
	}
	if classifierFacts.Prediction == nil || classifierFacts.Release != c.release || classifierFacts.ReleaseSHA256 != c.releaseSHA256 || classifierFacts.InputTokens < 1 || classifierFacts.InputTokens > 32768 || classifierFacts.HistorySource != router.ClassifierHistoricalPrediction {
		return router.ClassifierPrediction{}, fmt.Errorf("invalid classifier facts or release identity: %w", router.ErrClassifierUnavailable)
	}
	prediction := router.ClassifierPrediction{Complexity: *classifierFacts.Prediction, Probabilities: classifierFacts.Probabilities}
	return prediction, prediction.Validate()
}
