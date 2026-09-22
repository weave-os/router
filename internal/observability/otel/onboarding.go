package otel

import "weave-os/router/internal/auth"

// OnboardingObserver exports auth lifecycle facts through the shared span queue.
type OnboardingObserver struct {
	emitter *Emitter
}

var _ auth.OnboardingObserver = (*OnboardingObserver)(nil)

// NewOnboardingObserver wraps the emitter; a nil emitter disables export.
func NewOnboardingObserver(emitter *Emitter) *OnboardingObserver {
	return &OnboardingObserver{emitter: emitter}
}

// APIKeyFirstUsed maps a routing key's first authentication to an onboarding span.
func (o *OnboardingObserver) APIKeyFirstUsed(event auth.APIKeyFirstUsedEvent) {
	buf := o.emitter.NewBuffer()
	if buf == nil {
		return
	}
	attrs := NewAttrBuilder(4).
		String("external_id", event.InstallationExternalID).
		String("router_api_key_id", event.APIKeyID).
		String("harness", event.Harness)
	if event.CredentialSubjectID != "" {
		attrs = attrs.String("credential_subject_id", event.CredentialSubjectID)
	}
	buf.Record(Span{Name: "router.harness_connected", Start: event.OccurredAt, End: event.OccurredAt, Attrs: attrs.Build()})
	buf.Flush()
}

// SubscriptionConnected records the enrollment or adoption of an account.
func (o *OnboardingObserver) SubscriptionConnected(event auth.SubscriptionConnectedEvent) {
	buf := o.emitter.NewBuffer()
	if buf == nil {
		return
	}
	attrs := NewAttrBuilder(5).
		String("external_id", event.InstallationExternalID).
		String("router_api_key_id", event.APIKeyID).
		String("subscription_account_id", event.AccountID).
		String("provider", string(event.Provider))
	if event.CredentialSubjectID != "" {
		attrs = attrs.String("credential_subject_id", event.CredentialSubjectID)
	}
	buf.Record(Span{Name: "router.subscription_connected", Start: event.OccurredAt, End: event.OccurredAt, Attrs: attrs.Build()})
	buf.Flush()
}
