package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"

	"github.com/google/uuid"
)

// ResetCredit is an available Codex rate-limit reset. A zero expiry never expires.
type ResetCredit struct {
	ID        string
	ExpiresAt time.Time
}

// ResetOutcome is the provider's redemption result.
type ResetOutcome string

const (
	ResetApplied         ResetOutcome = "reset"
	ResetNothingToReset  ResetOutcome = "nothing_to_reset"
	ResetNoCredit        ResetOutcome = "no_credit"
	ResetAlreadyRedeemed ResetOutcome = "already_redeemed"
)

// CodexResetClient accesses quota and earned resets without running inference.
type CodexResetClient interface {
	CodexQuotaExhausted(context.Context, Lease) (bool, error)
	CodexResetCredits(context.Context, Lease) ([]ResetCredit, error)
	ConsumeCodexReset(context.Context, Lease, string, string) (ResetOutcome, error)
}

// ResetClaim serializes redemption across a subscriber's sessions and installations.
// Pending selections survive crashes so uncertain POSTs reuse the same credit and key.
type ResetClaim struct {
	SubscriberID string
	LeaseID      string
	AccountID    string
	CreditID     string
	RequestID    string
}

// ResetStore persists the selection before the irreversible provider call.
type ResetStore interface {
	AcquireCodexReset(context.Context, string, string, time.Duration) (ResetClaim, bool, error)
	SelectCodexReset(context.Context, ResetClaim) error
	RecoverCodexAccount(context.Context, ResetClaim, string) error
	ClearCodexReset(context.Context, ResetClaim) error
	ReleaseCodexReset(context.Context, ResetClaim) error
}

// ErrResetClaimLost prevents a stale worker from consuming or publishing a reset.
var ErrResetClaimLost = errors.New("Codex reset claim lost")

const resetOperationTimeout = 10 * time.Second

// CodexAutoUsageResetEnabled applies the flag and optional personal rollout scope
// to the authenticated subscriber, never to a client-supplied identity header.
func CodexAutoUsageResetEnabled(ctx context.Context, subscriberID string) bool {
	if subscriberID == "" || !flags.BoolOr(ctx, flags.KeyCodexAutoUsageReset, false) {
		return false
	}
	allowed := flags.StringOr(ctx, flags.KeyCodexAutoUsageResetSubscribers, "")
	if allowed == "" {
		return true
	}
	for _, candidate := range strings.Split(allowed, ",") {
		if strings.TrimSpace(candidate) == subscriberID {
			return true
		}
	}
	return false
}

// WithCodexResets wires earned-reset redemption; the per-installation flag stays off by default.
func (r *Runtime) WithCodexResets(client CodexResetClient, store ResetStore) *Runtime {
	r.resetClient, r.resetStore, r.resetTieChoice = client, store, rand.IntN
	return r
}

// ResetCodex recovers personal capacity only after live quota confirms exhaustion.
// It consumes at most one credit per call; shared accounts never donate reset credits.
func (r *Runtime) ResetCodex(ctx context.Context, owner auth.SubscriptionOwner, sessionID string) (lease Lease, recovered bool, err error) {
	if !CodexAutoUsageResetEnabled(ctx, owner.SubscriberID) || r.resetClient == nil || r.resetStore == nil {
		return Lease{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resetOperationTimeout)
	defer cancel()
	claim, acquired, err := r.resetStore.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), 3*resetOperationTimeout)
	if err != nil || !acquired {
		return Lease{}, false, err
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), refreshReleaseTimeout)
		defer releaseCancel()
		err = errors.Join(err, r.resetStore.ReleaseCodexReset(releaseCtx, claim))
		if err != nil {
			observability.FromContext(ctx).Warn("Codex earned reset recovery failed", "err", err)
		}
	}()
	store, ok := r.store.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	})
	if !ok {
		return Lease{}, false, errors.New("subscription candidate admission is not configured")
	}
	accounts, err := store.ListSubscriptionCandidates(ctx, owner)
	if err != nil {
		return Lease{}, false, err
	}
	var exhausted []Lease
	for _, account := range accounts {
		if account.Provider != auth.SubscriptionProviderCodex || account.SubscriberID != owner.SubscriberID || !account.Enabled || !resetAccountStateEligible(account.State) {
			continue
		}
		credential, credentialErr := r.resetCredential(ctx, owner, account)
		if credentialErr != nil {
			return Lease{}, false, credentialErr
		}
		spent, quotaErr := r.resetClient.CodexQuotaExhausted(ctx, credential)
		if quotaErr != nil {
			return Lease{}, false, quotaErr
		}
		if !spent {
			return r.recoverResetAccount(ctx, owner, sessionID, claim, credential)
		}
		exhausted = append(exhausted, credential)
	}
	if len(exhausted) == 0 {
		return Lease{}, false, nil
	}
	var selected Lease
	if claim.CreditID != "" {
		for _, account := range exhausted {
			if account.AccountID == claim.AccountID {
				selected = account
				break
			}
		}
		// An uncertain redemption remains reserved even after account access changes.
		if selected.AccountID == "" {
			return Lease{}, false, nil
		}
	} else {
		var candidates []resetCandidate
		for _, account := range exhausted {
			credits, listErr := r.resetClient.CodexResetCredits(ctx, account)
			if listErr != nil {
				return Lease{}, false, listErr
			}
			for _, credit := range credits {
				if credit.ID != "" && (credit.ExpiresAt.IsZero() || credit.ExpiresAt.After(r.clock())) {
					candidates = append(candidates, resetCandidate{account: account, credit: credit})
				}
			}
		}
		winner, found := earliestReset(candidates, r.resetTieChoice)
		if !found {
			return Lease{}, false, nil
		}
		selected = winner.account
		claim.AccountID, claim.CreditID = selected.AccountID, winner.credit.ID
		claim.RequestID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("weave-codex-reset:"+claim.AccountID+":"+claim.CreditID)).String()
		if err := r.resetStore.SelectCodexReset(ctx, claim); err != nil {
			return Lease{}, false, err
		}
	}
	// Re-read admission immediately before spending a credit, including resumed claims.
	if err := r.admitReset(ctx, owner, selected.AccountID); err != nil {
		return Lease{}, false, err
	}
	outcome, err := r.resetClient.ConsumeCodexReset(ctx, selected, claim.CreditID, claim.RequestID)
	if err != nil {
		return Lease{}, false, err
	}
	observability.FromContext(ctx).Info("Codex earned reset redemption completed", "account_id", selected.AccountID, "outcome", outcome)
	if outcome == ResetNoCredit {
		return Lease{}, false, r.resetStore.ClearCodexReset(ctx, claim)
	}
	switch outcome {
	case ResetApplied, ResetAlreadyRedeemed, ResetNothingToReset:
	default:
		return Lease{}, false, fmt.Errorf("unknown Codex reset outcome: %q", outcome)
	}
	spent, err := r.resetClient.CodexQuotaExhausted(ctx, selected)
	if err != nil || spent {
		return Lease{}, false, err
	}
	return r.recoverResetAccount(ctx, owner, sessionID, claim, selected)
}

func (r *Runtime) resetCredential(ctx context.Context, owner auth.SubscriptionOwner, account *auth.SubscriptionAccount) (Lease, error) {
	credential, err := r.refreshCredentials(owner, true)(ctx, Account{ID: account.ID, OwnerID: "account:" + account.ID,
		Provider: ProviderCodex, AccountID: account.ExternalAccountID, Enabled: account.Enabled, State: account.State})
	if err != nil {
		return Lease{}, err
	}
	return Lease{AccountID: account.ID, OwnerID: owner.SubscriberID, Tier: auth.SubscriptionTierPersonal,
		AccessToken: credential.AccessToken, ProviderAccount: account.ExternalAccountID, State: credential.State}, nil
}

func (r *Runtime) recoverResetAccount(ctx context.Context, owner auth.SubscriptionOwner, sessionID string, claim ResetClaim, lease Lease) (Lease, bool, error) {
	if err := r.admitReset(ctx, owner, lease.AccountID); err != nil {
		return Lease{}, false, err
	}
	if err := r.resetStore.RecoverCodexAccount(ctx, claim, lease.AccountID); err != nil {
		return Lease{}, false, err
	}
	lease.State = auth.SubscriptionAccountStateActive
	r.manager.pool("account:"+lease.AccountID, ProviderCodex).restoreQuota(lease.AccountID)
	if sessionID != "" {
		r.affinity.Add(owner.InstallationID+"|"+owner.SubscriberID+"|"+string(ProviderCodex)+"|"+sessionID, lease.AccountID)
	}
	return lease, true, nil
}

func (r *Runtime) admitReset(ctx context.Context, owner auth.SubscriptionOwner, accountID string) error {
	store, ok := r.store.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	})
	if !ok {
		return errors.New("subscription candidate admission is not configured")
	}
	accounts, err := store.ListSubscriptionCandidates(ctx, owner)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if account.ID == accountID && account.SubscriberID == owner.SubscriberID && account.Provider == auth.SubscriptionProviderCodex && account.Enabled && resetAccountStateEligible(account.State) {
			return nil
		}
	}
	return ErrNoAvailableAccount
}

type resetCandidate struct {
	account Lease
	credit  ResetCredit
}

func resetAccountStateEligible(state auth.SubscriptionAccountState) bool {
	switch state {
	case auth.SubscriptionAccountStateActive, auth.SubscriptionAccountStateUnknown, auth.SubscriptionAccountStateCooldown, auth.SubscriptionAccountStateExhausted:
		return true
	default:
		return false
	}
}

func earliestReset(candidates []resetCandidate, choose func(int) int) (resetCandidate, bool) {
	var earliest []resetCandidate
	for _, candidate := range candidates {
		if len(earliest) == 0 || !candidate.credit.ExpiresAt.IsZero() && (earliest[0].credit.ExpiresAt.IsZero() || candidate.credit.ExpiresAt.Before(earliest[0].credit.ExpiresAt)) {
			earliest = []resetCandidate{candidate}
		} else if candidate.credit.ExpiresAt.Equal(earliest[0].credit.ExpiresAt) {
			earliest = append(earliest, candidate)
		}
	}
	if len(earliest) == 0 {
		return resetCandidate{}, false
	}
	return earliest[choose(len(earliest))], true
}
