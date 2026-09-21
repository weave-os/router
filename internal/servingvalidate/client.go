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

// TokenSource mints a Google identity token only for an explicitly approved service audience.
type TokenSource func(context.Context, string) (string, error)

// Client never follows redirects or infers trusted origins from an artifact being validated.
type Client struct {
	http    *http.Client
	token   TokenSource
	origins map[string]struct{}
}

// New binds validation to origins supplied by trusted workflow configuration, not proposal contents.
func New(client *http.Client, token TokenSource, allowedOrigins []string) (*Client, error) {
	if client == nil || token == nil || len(allowedOrigins) == 0 {
		return nil, errors.New("private validation requires an HTTP client, IAM token source and approved origins")
	}
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" {
			return nil, errors.New("validation origins must be exact credential-free HTTPS origins without a trailing slash")
		}
		origins[origin] = struct{}{}
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	bounded.Timeout = privateValidationTimeout
	return &Client{http: &bounded, token: token, origins: origins}, nil
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
	if _, approved := c.origins[revision.URL]; !approved {
		return errors.New("revision URL is not an approved private validation origin")
	}
	if _, approved := c.origins[revision.Audience]; !approved {
		return errors.New("revision audience is not an approved private validation origin")
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
