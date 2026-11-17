package otel

import (
	"weave-os/router/internal/auth"
	"weave-os/router/internal/telemetry"
)

type OnboardingObserver struct {
	emitter *Emitter
}

var _ auth.OnboardingObserver = (*OnboardingObserver)(nil)

func NewOnboardingObserver(emitter *Emitter) *OnboardingObserver {
	return &OnboardingObserver{emitter: emitter}
}

func (o *OnboardingObserver) APIKeyFirstUsed(event auth.APIKeyFirstUsedEvent) {
	buf := o.emitter.NewBuffer()
	if buf == nil {
		return
	}
	attrs := telemetry.NewAttrBuilder(4).
		String("external_id", event.InstallationExternalID).
		String("router_api_key_id", event.APIKeyID).
		String("harness", event.Harness)
	if event.CredentialSubjectID != "" {
		attrs = attrs.String("credential_subject_id", event.CredentialSubjectID)
	}
	buf.Record(telemetry.Span{Name: "router.harness_connected", Start: event.OccurredAt, End: event.OccurredAt, Attrs: attrs.Build()})
	buf.Flush()
}

func (o *OnboardingObserver) SubscriptionConnected(event auth.SubscriptionConnectedEvent) {
	buf := o.emitter.NewBuffer()
	if buf == nil {
		return
	}
	attrs := telemetry.NewAttrBuilder(5).
		String("external_id", event.InstallationExternalID).
		String("router_api_key_id", event.APIKeyID).
		String("subscription_account_id", event.AccountID).
		String("provider", string(event.Provider))
	if event.CredentialSubjectID != "" {
		attrs = attrs.String("credential_subject_id", event.CredentialSubjectID)
	}
	buf.Record(telemetry.Span{Name: "router.subscription_connected", Start: event.OccurredAt, End: event.OccurredAt, Attrs: attrs.Build()})
	buf.Flush()
}

func (o *OnboardingObserver) HarnessLifecycle(event auth.HarnessLifecycleEvent) {
	if event.Action == auth.HarnessLifecycleActionConnected {
		o.APIKeyFirstUsed(auth.APIKeyFirstUsedEvent{
			InstallationExternalID: event.InstallationExternalID,
			CredentialSubjectID:    event.CredentialSubjectID,
			APIKeyID:               event.APIKeyID,
			Harness:                string(event.Harness),
			OccurredAt:             event.OccurredAt,
		})
		return
	}
	buf := o.emitter.NewBuffer()
	if buf == nil {
		return
	}
	attrs := telemetry.NewAttrBuilder(5).
		String("external_id", event.InstallationExternalID).
		String("router_api_key_id", event.APIKeyID).
		String("harness", string(event.Harness)).
		String("action", string(event.Action))
	if event.CredentialSubjectID != "" {
		attrs = attrs.String("credential_subject_id", event.CredentialSubjectID)
	}
	buf.Record(telemetry.Span{Name: "router.harness_lifecycle", Start: event.OccurredAt, End: event.OccurredAt, Attrs: attrs.Build()})
	buf.Flush()
}
