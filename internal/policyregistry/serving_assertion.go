package policyregistry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ServingAssertionHeader is reserved for the authenticated gateway-to-worker hop.
const ServingAssertionHeader = "X-Weave-Serving-Assertion"

// ServerlessAuthorizationHeader carries IAM separately from client subscription credentials.
const ServerlessAuthorizationHeader = "X-Serverless-Authorization"

// ServingAssertionV1 binds one admission to the exact original request and credential.
const ServingAssertionV1 ServingSchema = "router_serving_assertion_v1"

const assertionLifetime = 2 * time.Minute
const maxAssertionBytes = 16 * 1024

// ServingAssertion is request-scoped authority; it is never accepted directly from a client.
// The signature complements private IAM ingress and limits validation identities to their own endpoints.
type ServingAssertion struct {
	SchemaVersion    ServingSchema         `json:"schema_version"`
	APIKeyID         string                `json:"api_key_id"`
	Scope            AdmissionScope        `json:"scope"`
	Admission        SessionReleaseBinding `json:"admission"`
	Method           string                `json:"method"`
	RequestURI       string                `json:"request_uri"`
	BodySHA256       string                `json:"body_sha256"`
	CredentialSHA256 string                `json:"credential_sha256"`
	IssuedAt         time.Time             `json:"issued_at"`
	ExpiresAt        time.Time             `json:"expires_at"`
}

// AssertionSigner requires a separate shared gateway/worker key, supplied through a secret reference.
// No signing key is needed by legacy workers or artifact preparation/validation identities.
type AssertionSigner struct {
	key   []byte
	clock func() time.Time
}

// NewAssertionSigner rejects weak/missing keys rather than allowing unsigned managed traffic.
func NewAssertionSigner(key []byte, clock func() time.Time) (*AssertionSigner, error) {
	if len(key) < 32 || clock == nil {
		return nil, errors.New("serving assertion signing requires at least 32 key bytes and a clock")
	}
	return &AssertionSigner{key: append([]byte(nil), key...), clock: clock}, nil
}

// Sign binds the admitted selection to request bytes, not mutable client release headers.
func (s *AssertionSigner) Sign(assertion ServingAssertion, request *http.Request, body []byte, credential string) (string, error) {
	assertion.SchemaVersion = ServingAssertionV1
	assertion.Method = request.Method
	assertion.RequestURI = request.URL.RequestURI()
	assertion.BodySHA256 = Digest(body)
	assertion.CredentialSHA256 = Digest([]byte(credential))
	assertion.IssuedAt = s.clock().UTC()
	assertion.ExpiresAt = assertion.IssuedAt.Add(assertionLifetime)
	if err := assertion.validate(); err != nil {
		return "", err
	}
	payload, err := CanonicalBytes(assertion)
	if err != nil {
		return "", err
	}
	if len(payload) > maxAssertionBytes {
		return "", errors.New("serving assertion exceeds size bound")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify authenticates admission before any worker snapshot or provider lookup.
// Expiry is checked only at entry; it does not cancel an already admitted stream.
func (s *AssertionSigner) Verify(encoded string, request *http.Request, body []byte, credential string) (ServingAssertion, error) {
	var assertion ServingAssertion
	if len(encoded) > 2*maxAssertionBytes {
		return assertion, errors.New("serving assertion exceeds size bound")
	}
	payload64, signature64, ok := strings.Cut(encoded, ".")
	if !ok {
		return assertion, errors.New("signed serving assertion required")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payload64)
	if err != nil || len(payload) > maxAssertionBytes {
		return assertion, errors.New("invalid serving assertion encoding")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signature64)
	if err != nil {
		return assertion, errors.New("invalid serving assertion signature")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return assertion, errors.New("invalid serving assertion signature")
	}
	if err := strictDecode(payload, &assertion); err != nil {
		return ServingAssertion{}, err
	}
	if err := assertion.validate(); err != nil {
		return ServingAssertion{}, err
	}
	now := s.clock().UTC()
	if now.Before(assertion.IssuedAt) || !now.Before(assertion.ExpiresAt) || assertion.ExpiresAt.Sub(assertion.IssuedAt) != assertionLifetime {
		return ServingAssertion{}, errors.New("serving assertion is outside its admission window")
	}
	if assertion.Method != request.Method || assertion.RequestURI != request.URL.RequestURI() || assertion.BodySHA256 != Digest(body) || assertion.CredentialSHA256 != Digest([]byte(credential)) {
		return ServingAssertion{}, errors.New("serving assertion does not match this request")
	}
	return assertion, nil
}

func (a ServingAssertion) validate() error {
	if a.SchemaVersion != ServingAssertionV1 || a.APIKeyID == "" || a.Scope.InstallationID == "" || a.Scope.CredentialIdentity == "" || a.Admission.BindingGeneration <= 0 || a.Admission.ActivationID == "" || a.Method == "" || a.RequestURI == "" || a.IssuedAt.IsZero() || a.ExpiresAt.IsZero() {
		return errors.New("incomplete serving assertion")
	}
	if _, err := a.Admission.Target.Environment(); err != nil {
		return err
	}
	if !validDigest(a.BodySHA256) || !validDigest(a.CredentialSHA256) {
		return errors.New("invalid request identity in serving assertion")
	}
	if a.Scope.Persistent == (a.Scope.ConversationDigest == [ConversationDigestLen]byte{}) {
		return errors.New("invalid persistent conversation scope")
	}
	return nil
}

// StripServingHeaders removes caller assertions and IAM tokens before authentication/forwarding.
func StripServingHeaders(headers http.Header) {
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-weave-serving-") || strings.HasPrefix(lower, "x-weave-internal-") || strings.EqualFold(name, ServerlessAuthorizationHeader) {
			delete(headers, name)
		}
	}
}
