package providers

import (
	"context"
	"errors"
)

var ErrModelDiscoveryDestination = errors.New("model discovery destination is not allowed")

var ErrModelDiscoveryTransport = errors.New("model discovery endpoint could not be reached")

type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}
