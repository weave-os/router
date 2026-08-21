package providers

import "context"

// ModelLister is implemented by provider adapters that expose a model-listing endpoint.
// Request-context BYOK credentials win over the deployment-level key, mirroring inference.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}
