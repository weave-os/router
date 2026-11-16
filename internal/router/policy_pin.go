package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type PolicyPin struct {
	ArtifactSHA256 string
	RosterSHA256   string
}

const PolicyPinSeparator = "@"

type PolicyPinErrorCode string

const (
	PolicyPinErrorMalformed PolicyPinErrorCode = "policy_pin_malformed"
)

const PolicyPinUnavailableReason = "policy_pin_unavailable"

var ErrPolicyPinMalformed = errors.New("router: malformed policy pin")

var ErrPolicyPinUnavailable = errors.New("router: " + PolicyPinUnavailableReason)

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

func (p PolicyPin) String() string {
	return p.ArtifactSHA256 + PolicyPinSeparator + p.RosterSHA256
}

type PolicyPinRequest struct {
	Pin        PolicyPin
	Authorized bool
}

type policyPinContextKey struct{}

func WithPolicyPinRequest(ctx context.Context, request PolicyPinRequest) context.Context {
	return context.WithValue(ctx, policyPinContextKey{}, request)
}

func PolicyPinRequestFrom(ctx context.Context) (PolicyPinRequest, bool) {
	request, ok := ctx.Value(policyPinContextKey{}).(PolicyPinRequest)
	return request, ok
}

func HonouredPolicyPin(ctx context.Context) (PolicyPin, bool) {
	request, ok := PolicyPinRequestFrom(ctx)
	if !ok || !request.Authorized {
		return PolicyPin{}, false
	}
	return request.Pin, true
}
