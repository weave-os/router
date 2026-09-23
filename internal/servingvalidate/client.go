// Package servingvalidate is the private-endpoint validation adapter; it keeps IAM and HTTP outside registry contracts.
package servingvalidate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"weave-os/router/internal/policyregistry"
)

const (
	maxAttestationBytes      = 1 << 20
	privateValidationTimeout = 2 * time.Minute
)

// TokenSource mints a Google identity token for the revision's IAM service audience.
type TokenSource func(context.Context, string) (string, error)

// Client never follows redirects; the revisions it calls come from immutable, validated bindings.
type Client struct {
	http  *http.Client
	token TokenSource
}

// New bounds the HTTP client: no redirects, a fixed timeout, and a capped attestation body.
func New(client *http.Client, token TokenSource) (*Client, error) {
	if client == nil || token == nil {
		return nil, errors.New("private validation requires an HTTP client and IAM token source")
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	bounded.Timeout = privateValidationTimeout
	return &Client{http: &bounded, token: token}, nil
}

// ValidateWorker forces the exact revision to load and validate this immutable default/profile snapshot.
func (c *Client) ValidateWorker(ctx context.Context, revision policyregistry.RevisionBinding, selection policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	payload, err := json.Marshal(selection)
	if err != nil {
		return policyregistry.WorkerAttestation{}, err
	}
	var attestation policyregistry.WorkerAttestation
	err = c.call(ctx, revision, http.MethodPost, policyregistry.WorkerValidationPath, payload, &attestation)
	return attestation, err
}

// AttestClassifier requires the serving contract; legacy core-only readiness is not sufficient evidence.
func (c *Client) AttestClassifier(ctx context.Context, revision policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	var attestation policyregistry.ClassifierAttestation
	err := c.call(ctx, revision, http.MethodGet, policyregistry.ClassifierAttestationPath, nil, &attestation)
	return attestation, err
}

func (c *Client) call(ctx context.Context, revision policyregistry.RevisionBinding, method, path string, payload []byte, attestation any) error {
	for _, raw := range []string{revision.URL, revision.Audience} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("revision URL and audience must be credential-free HTTPS origins")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, privateValidationTimeout)
	defer cancel()
	token, err := c.token(ctx, revision.Audience)
	if err != nil {
		return fmt.Errorf("mint private validation identity: %w", err)
	}
	if token == "" {
		return errors.New("private validation identity token is empty")
	}
	request, err := http.NewRequestWithContext(ctx, method, revision.URL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("X-Serverless-Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call private validation endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("private validation endpoint returned HTTP %d; exact serving attestation support and authorized internal ingress are required", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxAttestationBytes+1))
	if err != nil {
		return fmt.Errorf("read private attestation: %w", err)
	}
	if len(encoded) > maxAttestationBytes {
		return errors.New("private attestation exceeds response size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(attestation); err != nil {
		return fmt.Errorf("decode private serving attestation: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("private attestation contains trailing JSON")
	}
	return nil
}
