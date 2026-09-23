package policyregistry

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/requestcontext"
)

// ConversationDigestLen preserves the existing conversation-preference digest width.
const ConversationDigestLen = 16

// ServingConversationDigest preserves legacy beta preference key bytes for shared credentials.
// Personal credentials supply the stable subject identity instead of the rotating key ID.
// No ID means no persisted binding; callers must not substitute a first-message hash.
func ServingConversationDigest(credentialIdentity, clientSessionID string) ([ConversationDigestLen]byte, bool) {
	var digest [ConversationDigestLen]byte
	if credentialIdentity == "" || clientSessionID == "" {
		return digest, false
	}
	return requestcontext.ConversationKey(credentialIdentity, clientSessionID, requestcontext.LegacyBetaConversationKey, digest), true
}

// AdmissionScope is the authenticated storage key for a conversation, not a thread-model pin.
type AdmissionScope struct {
	InstallationID     string                      `json:"installation_id"`
	CredentialIdentity string                      `json:"credential_identity"`
	ConversationDigest [ConversationDigestLen]byte `json:"conversation_digest"`
	Persistent         bool                        `json:"persistent"`
}

// SerializedAdmission is held under primary-database identity and conversation locks.
type SerializedAdmission struct {
	Projection AdmissionProjection
	Previous   *SessionReleaseBinding
	Clock      func(context.Context) (time.Time, error)
}

// AdmissionDecision performs the authoritative GCS read before the DB transaction commits.
type AdmissionDecision func(context.Context, SerializedAdmission) (SessionReleaseBinding, error)

// ServingAdmissionStore serializes authentication projections and binding decisions together.
type ServingAdmissionStore interface {
	Admit(context.Context, string, string, string, AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error)
}

// RequestAttributionStore preserves the exact tuple behind the existing request-ID
// joins in telemetry, inference attempts, billing and feedback. It is not a head.
type RequestAttributionStore interface {
	RecordServingRequest(context.Context, string, ServingAssertion) error
}

// ServingAdmission resolves exact manifests; it cannot fail over to another target or a lane default.
type ServingAdmission struct {
	Store ServingStore
}

// Decide is called inside the primary transaction, after identity/session locks are acquired.
func (a ServingAdmission) Decide(ctx context.Context, admission SerializedAdmission) (SessionReleaseBinding, error) {
	if a.Store == nil || admission.Clock == nil {
		return SessionReleaseBinding{}, errors.New("serving admission registry and database clock are required")
	}
	snapshot, err := a.Store.ReadServingState(ctx, admission.Projection.Target)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	if err := snapshot.State.Validate(a.Store.RootURI(), admission.Projection.Target); err != nil {
		return SessionReleaseBinding{}, err
	}
	activationIDs := []string{snapshot.State.CurrentActivationID}
	if admission.Previous != nil && admission.Previous.Target == admission.Projection.Target && admission.Previous.ActivationID != snapshot.State.CurrentActivationID {
		activationIDs = append(activationIDs, admission.Previous.ActivationID)
	}
	sets := make(map[string]SelectionSetView, len(activationIDs))
	for _, id := range activationIDs {
		activation, exists := snapshot.State.Activations[id]
		if !exists {
			return SessionReleaseBinding{}, errors.New("session activation is unknown to authoritative target state")
		}
		if _, exists := sets[activation.SelectionSet.SHA256]; exists {
			continue
		}
		set, err := readSelectionSetView(ctx, a.Store, activation.SelectionSet)
		if err != nil {
			return SessionReleaseBinding{}, err
		}
		sets[activation.SelectionSet.SHA256] = set
	}
	// Sample after the authoritative read so an activation during GCS I/O cannot
	// appear to come from the future merely because transaction setup started earlier.
	now, err := admission.Clock(ctx)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	return SelectSessionRelease(admission.Previous, admission.Projection, snapshot, sets, a.Store.RootURI(), now)
}
