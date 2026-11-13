package providers

import (
	"context"
	"errors"
)

// ErrModelDiscoveryDestination is returned when model discovery targets an
// address outside its configured destination policy.
var ErrModelDiscoveryDestination = errors.New("model discovery destination is not allowed")

// ErrModelDiscoveryTransport is returned when a protected model-discovery
// request cannot reach its endpoint without exposing network details.
var ErrModelDiscoveryTransport = errors.New("model discovery endpoint could not be reached")

// ModelLister is implemented by provider adapters that expose a model-listing endpoint.
// Request-context BYOK credentials win over the deployment-level key, mirroring inference.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}
