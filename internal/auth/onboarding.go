package auth

import "time"

// OnboardingObserver receives committed onboarding transitions. Implementations
// must be nonblocking, concurrency-safe, and best-effort; delivery is not durable.
type OnboardingObserver interface {
	APIKeyFirstUsed(APIKeyFirstUsedEvent)
	SubscriptionConnected(SubscriptionConnectedEvent)
}

// APIKeyFirstUsedEvent records a routing key's first successful authentication,
// including validation probes. It does not imply a successful inference.
type APIKeyFirstUsedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	Harness                string
	OccurredAt             time.Time
}

// SubscriptionConnectedEvent records a newly enrolled or adopted account.
type SubscriptionConnectedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	AccountID              string
	Provider               SubscriptionProvider
	OccurredAt             time.Time
}
