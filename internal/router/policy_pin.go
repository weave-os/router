package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PolicyPin names the exact frozen policy artifact and declarative roster a
// turn must be served by: `<policy_artifact_sha256>@<roster_sha256>`.
type PolicyPin struct {
	ArtifactSHA256 string
	RosterSHA256   string
}

// PolicyPinSeparator splits the artifact digest from the roster digest.
const PolicyPinSeparator = "@"

// PolicyPinErrorCode is the typed client error code for an unparsable pin.
type PolicyPinErrorCode string

const (
	PolicyPinErrorMalformed PolicyPinErrorCode = "policy_pin_malformed"
)

// PolicyPinUnavailableReason is the typed 503 reason emitted when an honoured
// pin names an artifact or roster this deployment cannot serve.
const PolicyPinUnavailableReason = "policy_pin_unavailable"

// ErrPolicyPinMalformed is the sentinel for a syntactically invalid pin.
var ErrPolicyPinMalformed = errors.New("router: malformed policy pin")

// ErrPolicyPinUnavailable is returned when a pin was honoured but the requested
// artifact or roster is not loaded; the turn fails closed rather than falling
// through to the current policy.
var ErrPolicyPinUnavailable = errors.New("router: " + PolicyPinUnavailableReason)

// ParsePolicyPin parses `<artifact_sha256>@<roster_sha256>`; both digests must
// be 64 hex characters. Digests are normalised to lowercase.
func ParsePolicyPin(raw string) (PolicyPin, error) {
	raw = strings.TrimSpace(raw)
	artifact, roster, found := strings.Cut(raw, PolicyPinSeparator)
	if !found {
		return PolicyPin{}, fmt.Errorf("%w: expected <artifact_sha256>%s<roster_sha256>", ErrPolicyPinMalformed, PolicyPinSeparator)
	}
	artifact, err := parseSHA256Digest(artifact, "artifact")
	if err != nil {
		return PolicyPin{}, err
	}
	roster, err = parseSHA256Digest(roster, "roster")
	if err != nil {
		return PolicyPin{}, err
	}
	return PolicyPin{ArtifactSHA256: artifact, RosterSHA256: roster}, nil
}

func parseSHA256Digest(value, label string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return "", fmt.Errorf("%w: %s sha256 must be 64 hex characters", ErrPolicyPinMalformed, label)
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("%w: %s sha256 must be hex", ErrPolicyPinMalformed, label)
		}
	}
	return value, nil
}

// String renders the pin in header form.
func (p PolicyPin) String() string {
	return p.ArtifactSHA256 + PolicyPinSeparator + p.RosterSHA256
}

// PolicyPinRequest is a parsed pin header together with whether the calling
// installation may have it honoured. An unauthorized request is recorded with
// a zero Pin (requested, not honoured, value never parsed) so replay analysis
// can see it was asked for without the header affecting the response.
type PolicyPinRequest struct {
	Pin        PolicyPin
	Authorized bool
}

type policyPinContextKey struct{}

// WithPolicyPinRequest stashes the request's pin state on ctx.
func WithPolicyPinRequest(ctx context.Context, request PolicyPinRequest) context.Context {
	return context.WithValue(ctx, policyPinContextKey{}, request)
}

// PolicyPinRequestFrom returns the pin state; ok is false when no pin header
// reached the router (the feature is off or the header was absent).
func PolicyPinRequestFrom(ctx context.Context) (PolicyPinRequest, bool) {
	request, ok := ctx.Value(policyPinContextKey{}).(PolicyPinRequest)
	return request, ok
}

// HonouredPolicyPin returns the pin the router must serve, or false when the
// request carries no authorized pin.
func HonouredPolicyPin(ctx context.Context) (PolicyPin, bool) {
	request, ok := PolicyPinRequestFrom(ctx)
	if !ok || !request.Authorized {
		return PolicyPin{}, false
	}
	return request.Pin, true
}
