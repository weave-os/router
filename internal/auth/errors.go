package auth

import "errors"

// ErrInvalidPrefix and ErrInvalidToken are distinct for internal telemetry.
// HTTP handlers collapse both to the same opaque 401.
var (
	ErrInvalidPrefix = errors.New("invalid bearer key prefix")
	ErrInvalidToken  = errors.New("invalid bearer key")

	// ErrWrongKeyScope is returned when a key is valid but its scope doesn't
	// cover the surface — analytics on data plane, or routing on export. Both collapse to 401.
	ErrWrongKeyScope = errors.New("bearer key scope not valid for this surface")

	// ErrInvalidKeyScope is returned when issuing a key with a scope the
	// router doesn't recognize.
	ErrInvalidKeyScope = errors.New("unknown api key scope")

	// ErrAPIKeyNotFound is returned when a rotate/delete targets a key that is
	// either missing or owned by a different installation.
	ErrAPIKeyNotFound = errors.New("api key not found")

	// ErrExternalAPIKeyNotFound is returned when a BYOK-key update matches no
	// row — missing, soft-deleted, or owned by another installation.
	ErrExternalAPIKeyNotFound = errors.New("external api key not found")

	// ErrUpstreamCredentialUnavailable is returned when a key exists but its credential cannot be
	// produced — derived-auth signing/attestation failed or the stored secret is empty.
	ErrUpstreamCredentialUnavailable = errors.New("upstream credential unavailable")

	// ErrInstallationNotFound is returned when an installation update matches no
	// row — a stale, soft-deleted, or cross-tenant id. Without it a zero-row
	// UPDATE looks like success, so the caller would invalidate the cache and
	// report the change as applied when nothing changed.
	ErrInstallationNotFound = errors.New("installation not found")
)
