package flags

import "context"

type overridesContextKey struct{}

// WithOverrides stashes per-organization overrides on ctx. An empty set leaves
// ctx unchanged so the accessors take their cheap path.
func WithOverrides(ctx context.Context, o Overrides) context.Context {
	if o.IsEmpty() {
		return ctx
	}
	return context.WithValue(ctx, overridesContextKey{}, o)
}

// OverridesFromContext returns the per-organization overrides carried by ctx.
func OverridesFromContext(ctx context.Context) (o Overrides, ok bool) {
	o, ok = ctx.Value(overridesContextKey{}).(Overrides)
	return o, ok
}

// BoolOr returns the per-organization override for key, or deploymentDefault
// when the request carries none.
func BoolOr(ctx context.Context, key Key, deploymentDefault bool) (value bool) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Bools[key]; found {
		return v
	}
	return deploymentDefault
}

// IntOr returns the per-organization override for key, or deploymentDefault
// when the request carries none.
func IntOr(ctx context.Context, key Key, deploymentDefault int) (value int) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Ints[key]; found {
		return v
	}
	return deploymentDefault
}

// FloatOr returns the per-organization override for key, or deploymentDefault
// when the request carries none.
func FloatOr(ctx context.Context, key Key, deploymentDefault float64) (value float64) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Floats[key]; found {
		return v
	}
	return deploymentDefault
}

// StringOr returns the per-organization override for key, or deploymentDefault
// when the request carries none.
func StringOr(ctx context.Context, key Key, deploymentDefault string) (value string) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Strings[key]; found {
		return v
	}
	return deploymentDefault
}
