package providers

import "context"

// ModelLister is implemented by provider adapters whose upstream exposes a
// model-listing endpoint. ListModels returns the model IDs the endpoint
// publishes, resolving credentials exactly as an inference call would
// (request-context BYOK credentials win over the deployment-level key).
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}
