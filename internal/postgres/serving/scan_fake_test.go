package serving_test

import "fmt"

func scanInto[T any](dest any, value T) error {
	target, ok := dest.(*T)
	if !ok {
		return fmt.Errorf("scan target is %T, want *%T", dest, value)
	}
	*target = value
	return nil
}
