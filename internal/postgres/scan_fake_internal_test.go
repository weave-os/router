package postgres

import "fmt"

func scanInto[T any](dest any, value T) error {
	target, ok := dest.(*T)
	if !ok {
		return fmt.Errorf("scan target is %T, want *%T", dest, value)
	}
	*target = value
	return nil
}

func setFrom[T any](target *T, value any) error {
	got, ok := value.(T)
	if !ok {
		return fmt.Errorf("scan value is %T, want %T", value, got)
	}
	*target = got
	return nil
}
