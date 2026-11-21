package policyregistry

import (
	"net/http"
	"strings"
	"time"
)

const ServingAssertionHeader = "X-Weave-Serving-Assertion"

const ServerlessAuthorizationHeader = "X-Serverless-Authorization"

type ServingAssertion struct {
	TestPlan         *TestPlanScope        `json:"test_plan,omitempty"`
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

func StripServingHeaders(headers http.Header) {
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-weave-serving-") || strings.HasPrefix(lower, "x-weave-internal-") || strings.EqualFold(name, ServerlessAuthorizationHeader) {
			delete(headers, name)
		}
	}
}
