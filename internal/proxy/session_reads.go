package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) getSessionPin(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	readCtx, finish, err := startDependency(ctx, requestcontext.DependencyDatabase)
	if err != nil {
		return sessionpin.Pin{}, false, err
	}
	pin, found, err := s.pinStore.Get(readCtx, key, role)
	finish(err)
	if err != nil {
		return pin, found, markDependencyFailure(ctx, requestcontext.DependencyDatabase, err)
	}
	return pin, found, nil
}
