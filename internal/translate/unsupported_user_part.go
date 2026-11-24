package translate

import (
	"errors"
	"fmt"
)

var ErrUnsupportedUserPart = errors.New("unsupported user content part")

type UnsupportedUserPartError struct {
	SourceFormat Format
	TargetFamily string
	PartType     string
	Reason       string
}

func (e *UnsupportedUserPartError) Error() string {
	msg := fmt.Sprintf("unsupported user content part %q for %s target", e.PartType, e.TargetFamily)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *UnsupportedUserPartError) Unwrap() error {
	return ErrUnsupportedUserPart
}

func newUnsupportedUserPart(src Format, target, partType, reason string) error {
	return &UnsupportedUserPartError{
		SourceFormat: src,
		TargetFamily: target,
		PartType:     partType,
		Reason:       reason,
	}
}

func NewUnsupportedUserPart(src Format, target, partType, reason string) error {
	return newUnsupportedUserPart(src, target, partType, reason)
}
