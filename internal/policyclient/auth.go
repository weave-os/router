package policyclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/credentials/idtoken"
	"cloud.google.com/go/auth/credentials/impersonate"
	"cloud.google.com/go/auth/httptransport"
)

const (
	cloudPlatformScope        = "https://www.googleapis.com/auth/cloud-platform"
	externalAccountCredsType  = "external_account"
	impersonationURLJSONField = "service_account_impersonation_url"
	// Cloud Run tagged revision URLs are <tag>---<service host>; IAM only
	// verifies ID tokens whose audience is the service host itself.
	cloudRunTagSeparator = "---"
)

// NewGoogleIDToken builds a Client that attaches a Google-signed ID token
// (audience = sidecar service origin, with any revision tag stripped) to every
// request; for Cloud Run sidecars only.
func NewGoogleIDToken(baseURL string, timeout time.Duration, opts ...Option) (*Client, error) {
	normalizedBaseURL, audience, err := googleIDTokenURLs(baseURL)
	if err != nil {
		return nil, fmt.Errorf("build Google ID-token policy client: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	idTokenCredentials, err := newGoogleIDTokenCredentials(audience)
	if err != nil {
		return nil, fmt.Errorf("build Google ID-token credentials for %q: %w", audience, err)
	}
	httpClient, err := newGoogleIDTokenHTTPClient(idTokenCredentials, timeout)
	if err != nil {
		return nil, fmt.Errorf("build Google ID-token HTTP client for %q: %w", audience, err)
	}
	return New(normalizedBaseURL, httpClient, timeout, opts...), nil
}

// idtoken.NewCredentials cannot mint ID tokens from workload identity
// federation credentials (it calls IAM generateIdToken without a bearer), so
// that case exchanges the federated token itself and impersonates the target
// service account explicitly.
func newGoogleIDTokenCredentials(audience string) (*auth.Credentials, error) {
	detected, err := credentials.DetectDefault(&credentials.DetectOptions{Scopes: []string{cloudPlatformScope}})
	if err != nil {
		return nil, err
	}
	federated, err := parseWorkloadIdentityFederation(detected.JSON())
	if err != nil {
		return nil, err
	}
	if federated == nil {
		return idtoken.NewCredentials(&idtoken.Options{Audience: audience})
	}
	sourceCredentials, err := credentials.DetectDefault(&credentials.DetectOptions{
		CredentialsJSON: federated.sourceCredentialsJSON,
		Scopes:          []string{cloudPlatformScope},
	})
	if err != nil {
		return nil, fmt.Errorf("build federated source credentials: %w", err)
	}
	return impersonate.NewIDTokenCredentials(&impersonate.IDTokenOptions{
		Audience:        audience,
		TargetPrincipal: federated.targetServiceAccount,
		IncludeEmail:    true,
		Credentials:     sourceCredentials,
	})
}

type workloadIdentityFederation struct {
	targetServiceAccount  string
	sourceCredentialsJSON []byte
}

// parseWorkloadIdentityFederation returns nil for credentials that are not an
// external_account file with service-account impersonation (metadata server,
// service-account key, user credentials).
func parseWorkloadIdentityFederation(credentialsJSON []byte) (*workloadIdentityFederation, error) {
	if len(credentialsJSON) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(credentialsJSON, &fields); err != nil {
		return nil, fmt.Errorf("parse application default credentials: %w", err)
	}
	var credentialsType string
	if raw, ok := fields["type"]; ok {
		if err := json.Unmarshal(raw, &credentialsType); err != nil {
			return nil, fmt.Errorf("parse application default credentials type: %w", err)
		}
	}
	rawImpersonationURL, hasImpersonation := fields[impersonationURLJSONField]
	if credentialsType != externalAccountCredsType || !hasImpersonation {
		return nil, nil
	}
	var impersonationURL string
	if err := json.Unmarshal(rawImpersonationURL, &impersonationURL); err != nil {
		return nil, fmt.Errorf("parse %s: %w", impersonationURLJSONField, err)
	}
	targetServiceAccount, _, _ := strings.Cut(path.Base(impersonationURL), ":")
	if targetServiceAccount == "" || targetServiceAccount == "." || targetServiceAccount == "/" {
		return nil, fmt.Errorf("%s %q does not name a service account", impersonationURLJSONField, impersonationURL)
	}
	delete(fields, impersonationURLJSONField)
	sourceCredentialsJSON, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("rebuild federated source credentials: %w", err)
	}
	return &workloadIdentityFederation{
		targetServiceAccount:  targetServiceAccount,
		sourceCredentialsJSON: sourceCredentialsJSON,
	}, nil
}

func googleIDTokenURLs(baseURL string) (string, string, error) {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		return "", "", fmt.Errorf("sidecar URL is empty")
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", "", fmt.Errorf("parse sidecar URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", "", fmt.Errorf("sidecar URL must be an absolute HTTP(S) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("sidecar URL must not contain a query or fragment")
	}
	audience := (&url.URL{Scheme: parsed.Scheme, Host: cloudRunServiceHost(parsed.Host)}).String()
	return normalized, audience, nil
}

func cloudRunServiceHost(host string) string {
	firstLabel, _, _ := strings.Cut(host, ".")
	tag, _, tagged := strings.Cut(firstLabel, cloudRunTagSeparator)
	if !tagged || tag == "" {
		return host
	}
	return strings.TrimPrefix(host, tag+cloudRunTagSeparator)
}

func newGoogleIDTokenHTTPClient(idTokenCredentials *auth.Credentials, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if err := httptransport.AddAuthorizationMiddleware(client, idTokenCredentials); err != nil {
		return nil, err
	}
	return client, nil
}
