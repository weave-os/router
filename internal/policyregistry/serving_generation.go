package policyregistry

import "errors"

var ErrStaleServingGeneration = errors.New("serving learned state belongs to a newer binding generation")

func RejectStaleServingGeneration(storedGeneration, admittedGeneration int64) error {
	if storedGeneration > admittedGeneration {
		return ErrStaleServingGeneration
	}
	return nil
}
