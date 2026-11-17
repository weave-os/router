package auth

import "errors"

var (
	ErrInvalidPrefix = errors.New("invalid bearer key prefix")
	ErrInvalidToken  = errors.New("invalid bearer key")

	ErrWrongKeyScope = errors.New("bearer key scope not valid for this surface")

	ErrInvalidKeyScope = errors.New("unknown api key scope")

	ErrAPIKeyNotFound = errors.New("api key not found")

	ErrExternalAPIKeyNotFound = errors.New("external api key not found")

	ErrUpstreamCredentialUnavailable = errors.New("upstream credential unavailable")

	ErrInstallationNotFound = errors.New("installation not found")
)
