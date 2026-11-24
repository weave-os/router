package providers

import (
	"errors"
	"net/http"
)

func IsUpstreamWatchdogError(err error) bool {
	return errors.Is(err, ErrUpstreamIdleTimeout) ||
		errors.Is(err, ErrUpstreamOutputStall) ||
		errors.Is(err, ErrUpstreamSlowThroughput)
}

func UpstreamStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var statusErr *UpstreamStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status
	}
	var bufferedErr *UpstreamErrorResponse
	if errors.As(err, &bufferedErr) {
		return bufferedErr.Status
	}
	return 0
}
