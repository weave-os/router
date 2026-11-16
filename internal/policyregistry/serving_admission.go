package policyregistry

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/requestcontext"
)

const ConversationDigestLen = 16

func ServingConversationDigest(credentialIdentity, clientSessionID string) ([ConversationDigestLen]byte, bool) {
	var digest [ConversationDigestLen]byte
	if credentialIdentity == "" || clientSessionID == "" {
		return digest, false
	}
	return requestcontext.ConversationKey(credentialIdentity, clientSessionID, requestcontext.LegacyBetaConversationKey, digest), true
}

type AdmissionScope struct {
	InstallationID     string                      `json:"installation_id"`
	CredentialIdentity string                      `json:"credential_identity"`
	ConversationDigest [ConversationDigestLen]byte `json:"conversation_digest"`
	Persistent         bool                        `json:"persistent"`
}

type AdmissionTimings struct {
	StateRead         time.Duration
	SelectionSetRead  time.Duration
	SelectionSetReads int
}

func (t *AdmissionTimings) recordStateRead(elapsed time.Duration) {
	if t == nil {
		return
	}
	t.StateRead += elapsed
}

func (t *AdmissionTimings) recordSelectionSetRead(elapsed time.Duration) {
	if t == nil {
		return
	}
	t.SelectionSetRead += elapsed
	t.SelectionSetReads++
}

type SerializedAdmission struct {
	Projection AdmissionProjection
	Previous   *SessionReleaseBinding
	Clock      func(context.Context) (time.Time, error)
	Timings    *AdmissionTimings
}

type AdmissionDecision func(context.Context, SerializedAdmission) (SessionReleaseBinding, error)

type ServingAdmissionStore interface {
	Admit(context.Context, string, string, string, AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error)
}

type RequestAttributionStore interface {
	RecordServingRequest(context.Context, string, ServingAssertion) error
}

type ServingAdmission struct {
	Store ServingStore
}

func (a ServingAdmission) Decide(ctx context.Context, admission SerializedAdmission) (SessionReleaseBinding, error) {
	if a.Store == nil || admission.Clock == nil {
		return SessionReleaseBinding{}, errors.New("serving admission registry and database clock are required")
	}
	stateStart := time.Now()
	snapshot, err := a.Store.ReadServingState(ctx, admission.Projection.Target)
	admission.Timings.recordStateRead(time.Since(stateStart))
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
		setStart := time.Now()
		set, err := readSelectionSetView(ctx, a.Store, activation.SelectionSet)
		admission.Timings.recordSelectionSetRead(time.Since(setStart))
		if err != nil {
			return SessionReleaseBinding{}, err
		}
		sets[activation.SelectionSet.SHA256] = set
	}
	now, err := admission.Clock(ctx)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	return SelectSessionRelease(admission.Previous, admission.Projection, snapshot, sets, a.Store.RootURI(), now)
}
