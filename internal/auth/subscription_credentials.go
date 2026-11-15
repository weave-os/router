package auth

import (
	"context"
	"errors"
	"time"
)

// SubscriptionCredentialRecord is the encrypted credential state for one
// enrolled account. It never crosses the auth service boundary in this form.
type SubscriptionCredentialRecord struct {
	ExternalAccountID      string
	ProviderUserID         string
	Provider               SubscriptionProvider
	RefreshTokenCiphertext []byte
	AccessTokenCiphertext  []byte
	AccessTokenExpiresAt   *time.Time
	TokenRefreshedAt       *time.Time
	TokenRefreshVersion    int64
	TokenRefreshLeaseID    string
	Enabled                bool
	State                  SubscriptionAccountState
	CooldownUntil          *time.Time
}

// SubscriptionCredentials is the decrypted credential state used by the
// subscription runtime. The access token is retained only in process memory
// after this method returns.
type SubscriptionCredentials struct {
	ProviderUserID       string
	RefreshToken         []byte
	AccessToken          []byte
	AccessTokenExpiresAt *time.Time
	TokenRefreshedAt     *time.Time
	TokenRefreshVersion  int64
	TokenRefreshLeaseID  string
	Enabled              bool
	State                SubscriptionAccountState
	CooldownUntil        *time.Time
}

// RefreshLeaseAcquisition is the outcome of one refresh-lease attempt.
// TookOver means an expired lease from a holder that never released was
// replaced. That holder may already have spent the refresh token, so the new
// holder must not treat a terminal provider error as proof the account is dead.
type RefreshLeaseAcquisition struct {
	Acquired bool
	TookOver bool
}

type SubscriptionCredentialEventKind string

const (
	SubscriptionCredentialEventRejected           SubscriptionCredentialEventKind = "credential_rejected"
	SubscriptionCredentialEventReconnectRequired  SubscriptionCredentialEventKind = "reconnect_required"
	SubscriptionCredentialEventPersistenceFailed  SubscriptionCredentialEventKind = "token_persistence_failed"
	SubscriptionCredentialEventSelected           SubscriptionCredentialEventKind = "account_selected"
	SubscriptionCredentialEventLeaseAttempted     SubscriptionCredentialEventKind = "account_lease_attempted"
	SubscriptionCredentialEventCandidateEvaluated SubscriptionCredentialEventKind = "candidate_evaluated"
	SubscriptionCredentialEventAttempted          SubscriptionCredentialEventKind = "account_attempted"
	SubscriptionCredentialEventAdmissionEvaluated SubscriptionCredentialEventKind = "account_admission_evaluated"
)

// SubscriptionCredentialRejectionInput describes one provider credential
// rejection. A refresh rejection sets LeaseID and ExpectedVersion so a
// refresher that lost its lease cannot strike replacement credentials.
type SubscriptionCredentialRejectionInput struct {
	Reason                string
	ProviderErrorType     string
	ProviderRequestID     string
	UpstreamStatus        int
	RequestID             string
	RejectedAt            time.Time
	Window                time.Duration
	InvalidateAccessToken bool
	LeaseID               string
	ExpectedVersion       int64
}

// SubscriptionCredentialRejection is an account's provider credential
// rejection history inside the current strike window. PreviousReason is the
// reason recorded by the strike before this one.
type SubscriptionCredentialRejection struct {
	Count              int
	FirstRejectedAt    time.Time
	PreviousReason     string
	RepeatedDisconnect bool
}

// SubscriptionRefreshRepository coordinates refresh leases and encrypted
// credential persistence across router replicas.
type SubscriptionRefreshRepository interface {
	TryAcquireSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (RefreshLeaseAcquisition, error)
	ExtendSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (int64, error)
	ReleaseSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string) error
	DisableSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64) error
	CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64, time.Time) error
	RecordSubscriptionCredentialRejection(context.Context, string, SubscriptionOwner, SubscriptionCredentialRejectionInput) (SubscriptionCredentialRejection, error)
	GetSubscriptionCredentialRecord(context.Context, string, SubscriptionOwner) (*SubscriptionCredentialRecord, error)
	PersistSubscriptionTokens(context.Context, string, SubscriptionOwner, string, int64, []byte, []byte, time.Time) error
}

type SubscriptionMaintenanceRefreshRepository interface {
	TryAcquireSubscriptionMaintenanceRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (RefreshLeaseAcquisition, error)
}

func (s *Service) TryAcquireSubscriptionMaintenanceRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, ttl time.Duration) (RefreshLeaseAcquisition, error) {
	repo, ok := s.subscriptionAccounts.(SubscriptionMaintenanceRefreshRepository)
	if !ok {
		return RefreshLeaseAcquisition{}, errors.New("subscription maintenance refresh is not configured")
	}
	return repo.TryAcquireSubscriptionMaintenanceRefreshLease(ctx, accountID, owner, leaseID, ttl)
}
