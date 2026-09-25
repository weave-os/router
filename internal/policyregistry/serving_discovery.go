package policyregistry

import (
	"encoding/base64"
	"errors"

	"github.com/google/uuid"
)

// DiscoverySelectionHeader carries exact release metadata over the private IAM hop.
// It is not an inference admission assertion.
const DiscoverySelectionHeader = "X-Weave-Internal-Discovery-Selection"

const maxDiscoverySelectionBytes = 16 * 1024

// EncodeDiscoverySelection permits environment-default targets and their assigned profiles.
func EncodeDiscoverySelection(request WorkerValidationRequest) (string, error) {
	if err := validateDiscoverySelection(request); err != nil {
		return "", err
	}
	payload, err := CanonicalBytes(request)
	if err != nil {
		return "", err
	}
	if base64.RawURLEncoding.EncodedLen(len(payload)) > maxDiscoverySelectionBytes {
		return "", errors.New("discovery selection exceeds size bound")
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeDiscoverySelection bounds and validates metadata before any registry access.
// The worker still validates its registry namespace and the exact immutable manifests.
func DecodeDiscoverySelection(encoded string) (WorkerValidationRequest, error) {
	if encoded == "" || len(encoded) > maxDiscoverySelectionBytes {
		return WorkerValidationRequest{}, errors.New("bounded discovery selection required")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return WorkerValidationRequest{}, errors.New("invalid discovery selection encoding")
	}
	var request WorkerValidationRequest
	if err := strictDecode(payload, &request); err != nil {
		return WorkerValidationRequest{}, err
	}
	if err := validateDiscoverySelection(request); err != nil {
		return WorkerValidationRequest{}, err
	}
	return request, nil
}

func validateDiscoverySelection(request WorkerValidationRequest) error {
	if request.Target != TargetStable && request.Target != TargetStaging {
		return errors.New("discovery requires a public serving target")
	}
	if (request.ProfileKey == "") != (request.Selection.Profile == nil) {
		return errors.New("discovery profile key and reference must be supplied together")
	}
	if request.ProfileKey != "" {
		key, err := uuid.Parse(request.ProfileKey)
		if err != nil || key == uuid.Nil || key.String() != request.ProfileKey {
			return errors.New("discovery profile key must be a UUID")
		}
		if err := validateArtifactRef(*request.Selection.Profile); err != nil {
			return err
		}
	}
	if err := validateArtifactRef(request.Selection.Release); err != nil {
		return err
	}
	return validateArtifactRef(request.Selection.Binding)
}
