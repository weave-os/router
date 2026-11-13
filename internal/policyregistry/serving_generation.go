package policyregistry

import "errors"

// ErrStaleServingGeneration is returned when an older stream tries to mutate rebound learned state.
var ErrStaleServingGeneration = errors.New("serving learned state belongs to a newer binding generation")

// RejectStaleServingGeneration prevents an in-flight old admission from overwriting rebound session state.
func RejectStaleServingGeneration(storedGeneration, admittedGeneration int64) error {
	if storedGeneration > admittedGeneration {
		return ErrStaleServingGeneration
	}
	return nil
}
