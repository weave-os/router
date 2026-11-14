package timing

import "context"

type providerWaitObserverKey struct{}

// WithProviderWaitObserver attaches a request-local observer. Adapters report
// dispatch separately from routing and response preludes so UI clients never
// attribute local preparation to an upstream provider.
func WithProviderWaitObserver(ctx context.Context, observe func(bool)) context.Context {
	return context.WithValue(ctx, providerWaitObserverKey{}, observe)
}

// BeginProviderWait reports an upstream HTTP dispatch and returns its cleanup.
// Cleanup is required on every attempt, including failures before any output.
func BeginProviderWait(ctx context.Context) func() {
	observe, _ := ctx.Value(providerWaitObserverKey{}).(func(bool))
	if observe == nil {
		return func() {}
	}
	observe(true)
	return func() { observe(false) }
}

func endProviderWait(ctx context.Context) {
	if observe, _ := ctx.Value(providerWaitObserverKey{}).(func(bool)); observe != nil {
		observe(false)
	}
}
