package subscriptions

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"

	"github.com/google/uuid"
)

// ResetCredit is an available Codex rate-limit reset. A zero expiry never expires.
type ResetCredit struct {
	ID        string
	ExpiresAt time.Time
}

// ResetOutcome codes that ConsumeCodexReset accepts; any other code is treated as an error.
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
// Accounts in owner.ExcludedAccountIDs cannot serve this request, so they are never
// returned, but their headroom still prevents spending a credit.
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
	scan, err := r.scanResetAccounts(ctx, owner)
	if err != nil {
		return Lease{}, false, err
	}
	if scan.headroom != nil {
		return r.recoverResetAccount(ctx, owner, sessionID, claim, *scan.headroom)
	}
	if scan.excludedHeadroom || len(scan.exhausted) == 0 {
		return Lease{}, false, nil
	}
	return r.redeemReset(ctx, owner, sessionID, claim, scan.exhausted)
}

type resetScan struct {
	headroom         *Lease
	excludedHeadroom bool
	exhausted        []Lease
}

func (r *Runtime) scanResetAccounts(ctx context.Context, owner auth.SubscriptionOwner) (resetScan, error) {
	store, err := r.candidateLister()
	if err != nil {
		return resetScan{}, err
	}
	accounts, err := store.ListSubscriptionCandidates(ctx, owner)
	if err != nil {
		return resetScan{}, err
	}
	var scan resetScan
	for _, account := range accounts {
		if account.Provider != auth.SubscriptionProviderCodex || account.SubscriberID != owner.SubscriberID || !account.Enabled || !resetAccountStateEligible(account.State) {
			continue
		}
		credential, credentialErr := r.resetCredential(ctx, owner, account)
		if credentialErr != nil {
			return resetScan{}, credentialErr
		}
		quotaExhausted, quotaErr := r.resetClient.CodexQuotaExhausted(ctx, credential)
		if quotaErr != nil {
			return resetScan{}, quotaErr
		}
		excluded := slices.Contains(owner.ExcludedAccountIDs, account.ID)
		switch {
		case !quotaExhausted && excluded:
			scan.excludedHeadroom = true
		case !quotaExhausted:
			scan.headroom = &credential
			return scan, nil
		case !excluded:
			scan.exhausted = append(scan.exhausted, credential)
		}
	}
	return scan, nil
}

func (r *Runtime) redeemReset(ctx context.Context, owner auth.SubscriptionOwner, sessionID string, claim ResetClaim, exhausted []Lease) (Lease, bool, error) {
	selected, selectedNow, found, err := r.selectResetCredit(ctx, &claim, exhausted)
	if err != nil || !found {
		return Lease{}, false, err
	}
	// Re-read admission immediately before spending a credit, including resumed claims.
	if err := r.admitReset(ctx, owner, selected.AccountID); err != nil {
		if selectedNow {
			// No request was sent for this new selection, so release the credit.
			err = errors.Join(err, r.resetStore.ClearCodexReset(ctx, claim))
		}
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
	quotaExhausted, err := r.resetClient.CodexQuotaExhausted(ctx, selected)
	if err != nil || quotaExhausted {
		return Lease{}, false, err
	}
	return r.recoverResetAccount(ctx, owner, sessionID, claim, selected)
}

// selectResetCredit resumes a pending selection or persists the earliest-expiring
// credit. selectedNow reports a selection made by this call.
func (r *Runtime) selectResetCredit(ctx context.Context, claim *ResetClaim, exhausted []Lease) (selected Lease, selectedNow, found bool, err error) {
	if claim.CreditID != "" {
		for _, account := range exhausted {
			if account.AccountID == claim.AccountID {
				return account, false, true, nil
			}
		}
		// An uncertain redemption remains reserved even after account access changes.
		return Lease{}, false, false, nil
	}
	var candidates []resetCandidate
	for _, account := range exhausted {
		credits, listErr := r.resetClient.CodexResetCredits(ctx, account)
		if listErr != nil {
			return Lease{}, false, false, listErr
		}
		for _, credit := range credits {
			if credit.ExpiresAt.IsZero() || credit.ExpiresAt.After(r.clock()) {
				candidates = append(candidates, resetCandidate{account: account, credit: credit})
			}
		}
	}
	winner, ok := earliestReset(candidates, r.resetTieChoice)
	if !ok {
		return Lease{}, false, false, nil
	}
	claim.AccountID, claim.CreditID = winner.account.AccountID, winner.credit.ID
	claim.RequestID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("weave-codex-reset:"+claim.AccountID+":"+claim.CreditID)).String()
	if err := r.resetStore.SelectCodexReset(ctx, *claim); err != nil {
		return Lease{}, false, false, err
	}
	return winner.account, true, true, nil
}

func (r *Runtime) resetCredential(ctx context.Context, owner auth.SubscriptionOwner, account *auth.SubscriptionAccount) (Lease, error) {
	credential, err := r.refresh(owner)(requestcontext.WithSubscriptionQuotaRecovery(ctx), Account{ID: account.ID, OwnerID: "account:" + account.ID,
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

type subscriptionCandidateLister interface {
	ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
}

func (r *Runtime) candidateLister() (subscriptionCandidateLister, error) {
	store, ok := r.store.(subscriptionCandidateLister)
	if !ok {
		return nil, errors.New("subscription candidate admission is not configured")
	}
	return store, nil
}

func (r *Runtime) admitReset(ctx context.Context, owner auth.SubscriptionOwner, accountID string) error {
	store, err := r.candidateLister()
	if err != nil {
		return err
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
