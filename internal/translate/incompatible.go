package translate

import "errors"

func IsIntrinsicallyIncompatible(err error) bool {
	return errors.Is(err, ErrGeminiSchemaIncompatible) ||
		errors.Is(err, ErrReasoningIncompatible) ||
		errors.Is(err, ErrGeminiUnsignedToolHistory)
}
