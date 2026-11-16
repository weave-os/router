package subscriptions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"

	"github.com/cenkalti/backoff/v5"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	refreshLeaseTTL           = 30 * time.Second
	refreshLeaseHeartbeat     = refreshLeaseTTL / 3
	refreshReleaseTimeout     = 2 * time.Second
	refreshWaitInitial        = 50 * time.Millisecond
	refreshWaitMaximum        = 200 * time.Millisecond
	refreshRetryLimit         = 3
	candidateExplorationEvery = 10
	candidateExplorationIdle  = 5 * time.Minute
)

var _ = [1]struct{}{}[RefreshHTTPTimeout/refreshLeaseTTL]

var errRefreshLeaseLost = errors.New("subscription refresh lease lost")

type AccountStore interface {
	TryAcquireSubscriptionMaintenanceRefreshLease(context.Context, auth.SubscriptionOwner, string, string, time.Duration) (auth.RefreshLeaseAcquisition, error)
	ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, auth.SubscriptionOwner, string, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, auth.SubscriptionOwner, string, time.Time) error
	TryAcquireSubscriptionRefreshLease(context.Context, auth.SubscriptionOwner, string, string, time.Duration) (auth.RefreshLeaseAcquisition, error)
	ExtendSubscriptionRefreshLease(context.Context, auth.SubscriptionOwner, string, string, time.Duration) (bool, error)
	ReleaseSubscriptionRefreshLease(context.Context, auth.SubscriptionOwner, string, string) error
	DisableSubscriptionAccountIfRefreshHolder(context.Context, auth.SubscriptionOwner, string, string, int64) error
	CooldownSubscriptionAccountIfRefreshHolder(context.Context, auth.SubscriptionOwner, string, string, int64, time.Time) error
	LoadSubscriptionCredentials(context.Context, auth.SubscriptionOwner, string) (auth.SubscriptionCredentials, error)
	PersistSubscriptionTokens(context.Context, auth.SubscriptionOwner, string, string, int64, []byte, []byte, time.Time) error
}

type Lease struct {
	AccountID       string
	OwnerID         string
	Tier            auth.SubscriptionTier
	AccessToken     string
	ProviderAccount string
	State           auth.SubscriptionAccountState
	CooldownUntil   time.Time
	release         func()
}

func (l Lease) AllowsPlanAttempt(plan providers.SubscriptionPlan, allowEnterpriseOverage bool, now time.Time) bool {
	return l.State != auth.SubscriptionAccountStateExhausted ||
		!l.CooldownUntil.IsZero() && !l.CooldownUntil.After(now) ||
		plan == providers.SubscriptionPlanEnterprise && allowEnterpriseOverage
}

func (l Lease) Release() {
	if l.release != nil {
		l.release()
	}
}

type Leaser interface {
	Lease(context.Context, auth.SubscriptionOwner, Provider, string) (Lease, bool, error)
	Cooldown(context.Context, auth.SubscriptionOwner, Provider, string, time.Time) error
	Disable(context.Context, auth.SubscriptionOwner, Provider, string) error
}

type Runtime struct {
	affinity           *expirable.LRU[string, string]
	store              AccountStore
	refresher          TokenRefresher
	manager            *Manager
	clock              func() time.Time
	leaseTTL           time.Duration
	heartbeat          time.Duration
	candidateSignal    func(string, Provider, time.Time) CandidateSignal
	selectionMu        sync.Mutex
	candidateLoads     map[string]*candidateLoad
	explorationBudgets *expirable.LRU[string, *explorationBudget]
	selectionSequence  uint64
	earlyRefresh       EarlyRefreshPolicy
}

type CandidateSignal struct {
	Observed     bool
	Exhausted    bool
	Billable     bool
	UsedFraction float64
	ResetAt      time.Time
	ObservedAt   time.Time
}

type CandidateSignalFunc func(accountID string, provider Provider, now time.Time) CandidateSignal

func NewRuntime(store AccountStore, refresher TokenRefresher, clock func() time.Time) *Runtime {
	if clock == nil {
		clock = time.Now
	}
	return &Runtime{affinity: expirable.NewLRU[string, string](4096, nil, 30*time.Minute),
		store: store, refresher: refresher, manager: NewManager(clock), clock: clock,
		leaseTTL: refreshLeaseTTL, heartbeat: refreshLeaseHeartbeat,
		candidateLoads:     make(map[string]*candidateLoad),
		explorationBudgets: expirable.NewLRU[string, *explorationBudget](4096, nil, 30*time.Minute),
	}
}

func (r *Runtime) WithCandidateSignal(signal CandidateSignalFunc) *Runtime {
	r.candidateSignal = signal
	return r
}

type refreshHolder struct {
	leaseID  string
	tookOver bool
}

func (r *Runtime) Lease(ctx context.Context, requester auth.SubscriptionOwner, provider Provider, sessionID string) (Lease, bool, error) {
	if !requester.Valid() || (provider != ProviderClaude && provider != ProviderCodex) {
		return Lease{}, false, nil
	}
	var accounts []*auth.SubscriptionAccount
	var err error
	if store, ok := r.store.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	}); ok {
		accounts, err = store.ListSubscriptionCandidates(ctx, requester)
	} else {
		return Lease{}, false, errors.New("subscription candidate admission is not configured")
	}
	if err != nil {
		return Lease{}, false, err
	}
	var candidateSignals map[string]CandidateSignal
	if r.candidateSignal != nil {
		candidateSignals = snapshotCandidateSignals(accounts, provider, r.candidateSignal, r.clock())
	}
	affinityKey := requester.InstallationID + "|" + requester.SubscriberID + "|" + string(provider) + "|" + sessionID
	preferred := ""
	if sessionID != "" {
		preferred, _ = r.affinity.Get(affinityKey)
	}
	present := false
	eligible := make([]*auth.SubscriptionAccount, 0, len(accounts))
	for _, candidate := range accounts {
		if requester.RequiredAccountID != "" && candidate.ID != requester.RequiredAccountID {
			continue
		}
		if candidate.SubscriberID == "" || Provider(candidate.Provider) != provider || slices.Contains(requester.ExcludedAccountIDs, candidate.ID) {
			continue
		}
		present = true
		var cooldown time.Time
		if candidate.CooldownUntil != nil {
			cooldown = *candidate.CooldownUntil
		}
		if candidate.Enabled && subscriptionAccountEligible(ctx, provider, candidate.State, cooldown, r.clock()) {
			eligible = append(eligible, candidate)
		}
	}
	for len(eligible) > 0 {
		if err := ctx.Err(); err != nil {
			return Lease{}, present, err
		}
		candidate, releaseReservation := r.reserveCandidate(ctx, eligible, requester, provider, preferred, candidateSignals)
		eligible = eligible[1:]
		owner := auth.SubscriptionOwner{SubscriberID: candidate.SubscriberID, APIKeyID: candidate.EnrolledByAPIKeyID, InstallationID: requester.InstallationID}
		poolID := "account:" + candidate.ID
		account := accountFromSubscription(candidate)
		if err := r.manager.Upsert(account); err != nil {
			releaseReservation()
			return Lease{}, present, err
		}
		if candidate.AccessTokenMissing {
			r.manager.InvalidateAccessToken(poolID, provider, candidate.ID)
		}
		leaseStarted := time.Now()
		leased, release, leaseErr := r.manager.Lease(ctx, poolID, provider, "", r.refresh(owner))
		observability.FromContext(ctx).Info("Subscription account lease completed", "subscription_event", auth.SubscriptionCredentialEventLeaseAttempted,
			"account_id", candidate.ID, "provider", provider, "tier", candidate.Tier,
			"lease_ms", time.Since(leaseStarted).Milliseconds(), "lease_succeeded", leaseErr == nil)
		if errors.Is(leaseErr, ErrNoAvailableAccount) {
			releaseReservation()
			continue
		}
		if leaseErr != nil {
			releaseReservation()
			return Lease{}, present, leaseErr
		}
		poolRelease := release
		var once sync.Once
		release = func() { once.Do(func() { poolRelease(); releaseReservation() }) }
		if sessionID != "" {
			r.affinity.Add(affinityKey, leased.ID)
		}
		return Lease{AccountID: leased.ID, OwnerID: candidate.SubscriberID, Tier: candidate.Tier, AccessToken: leased.AccessToken,
			ProviderAccount: leased.AccountID, State: leased.State, CooldownUntil: leased.CooldownTil, release: release}, true, nil
	}
	if present {
		return Lease{}, true, ErrNoAvailableAccount
	}
	return Lease{}, false, nil
}

func snapshotCandidateSignals(accounts []*auth.SubscriptionAccount, provider Provider, signal CandidateSignalFunc, now time.Time) map[string]CandidateSignal {
	signals := make(map[string]CandidateSignal, len(accounts))
	for _, account := range accounts {
		signals[account.ID] = signal(account.ID, provider, now)
	}
	return signals
}

func candidateSignalRank(signal CandidateSignal) int {
	switch {
	case !signal.Observed:
		return 1
	case signal.Exhausted || signal.Billable:
		return 2
	default:
		return 0
	}
}

func (r *Runtime) Cooldown(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string, resetAt time.Time) error {
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return err
	}
	r.manager.Cooldown("account:"+accountID, provider, accountID, resetAt)
	return r.store.UpdateSubscriptionAccountCooldown(ctx, physicalOwner, accountID, resetAt)
}
func (r *Runtime) Exhaust(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string, resetAt time.Time) error {
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return err
	}
	r.manager.Exhaust("account:"+accountID, provider, accountID, resetAt)
	_, err = r.updateAccountHealth(ctx, physicalOwner, accountID, auth.SubscriptionAccountStateExhausted, true, &resetAt)
	return err
}
func (r *Runtime) ReconnectRequired(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string) error {
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return err
	}
	r.manager.ReconnectRequired("account:"+accountID, provider, accountID)
	_, err = r.updateAccountHealth(ctx, physicalOwner, accountID, auth.SubscriptionAccountStateReconnectRequired, false, nil)
	return err
}
func (r *Runtime) Activate(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string) error {
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return err
	}
	r.manager.Activate("account:"+accountID, provider, accountID)
	_, err = r.updateAccountHealth(ctx, physicalOwner, accountID, auth.SubscriptionAccountStateActive, true, nil)
	return err
}
func (r *Runtime) Disable(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string) error {
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return err
	}
	r.manager.Disable("account:"+accountID, provider, accountID)
	return r.store.UpdateSubscriptionAccountState(ctx, physicalOwner, accountID, false, nil)
}
func (r *Runtime) updateAccountHealth(ctx context.Context, owner auth.SubscriptionOwner, accountID string, state auth.SubscriptionAccountState, enabled bool, cooldownUntil *time.Time) (bool, error) {
	if store, ok := r.store.(interface {
		UpdateSubscriptionAccountHealth(context.Context, auth.SubscriptionOwner, string, auth.SubscriptionAccountState, bool, *time.Time) (bool, error)
	}); ok {
		return store.UpdateSubscriptionAccountHealth(ctx, owner, accountID, state, enabled, cooldownUntil)
	}
	return false, r.store.UpdateSubscriptionAccountState(ctx, owner, accountID, enabled, cooldownUntil)
}

func (r *Runtime) persistRefreshedTokens(ctx context.Context, owner auth.SubscriptionOwner, account Account, holder refreshHolder, version int64, refreshed RefreshedToken) error {
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), refreshReleaseTimeout*refreshRetryLimit+time.Second)
	defer cancelSave()
	policy := backoff.NewExponentialBackOff()
	policy.InitialInterval = refreshWaitInitial
	policy.MaxInterval = refreshWaitMaximum
	attempt := 0
	_, persistErr := backoff.Retry(saveCtx, func() (struct{}, error) {
		attempt++
		attemptCtx, cancel := context.WithTimeout(saveCtx, refreshReleaseTimeout)
		err := r.store.PersistSubscriptionTokens(attemptCtx, owner, account.ID, holder.leaseID, version,
			[]byte(refreshed.RefreshToken), []byte(refreshed.AccessToken), refreshed.ExpiresAt)
		cancel()
		if err == nil {
			return struct{}{}, nil
		}
		if errors.Is(err, auth.ErrSubscriptionRefreshConflict) || errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
			return struct{}{}, backoff.Permanent(err)
		}
		observability.FromContext(ctx).Error("Failed to save rotated subscription tokens",
			"subscription_event", auth.SubscriptionCredentialEventPersistenceFailed,
			"owner_id", owner.LogKey(), "account_id", account.ID, "provider", account.Provider,
			"refresh_lease_id", holder.leaseID, "token_refresh_version", version,
			"lease_taken_over", holder.tookOver, "attempt", attempt, "retries_exhausted", attempt == refreshRetryLimit, "err", err)
		return struct{}{}, err
	}, backoff.WithBackOff(policy), backoff.WithMaxTries(refreshRetryLimit))
	return persistErr
}

func (r *Runtime) refreshWithHeartbeat(ctx context.Context, owner auth.SubscriptionOwner, account Account, leaseID, refreshToken string) (RefreshedToken, error) {
	refreshCtx, cancelRefresh := context.WithCancelCause(ctx)
	defer cancelRefresh(nil)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(r.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ticker.C:
			}
			extendCtx, cancelExtend := context.WithTimeout(context.WithoutCancel(ctx), refreshReleaseTimeout)
			extended, err := r.store.ExtendSubscriptionRefreshLease(extendCtx, owner, account.ID, leaseID, r.leaseTTL)
			cancelExtend()
			if err != nil {
				observability.FromContext(ctx).Warn("Failed to extend subscription refresh lease", "owner_id", owner.LogKey(), "account_id", account.ID, "provider", account.Provider, "err", err)
				continue
			}
			if !extended {
				cancelRefresh(errRefreshLeaseLost)
				return
			}
		}
	}()
	refreshed, err := r.refresher.Refresh(refreshCtx, account.Provider, refreshToken)
	close(stopHeartbeat)
	<-heartbeatDone
	if err != nil && errors.Is(context.Cause(refreshCtx), errRefreshLeaseLost) {
		return RefreshedToken{}, errRefreshLeaseLost
	}
	return refreshed, err
}

func (r *Runtime) handleRefreshError(ctx context.Context, owner auth.SubscriptionOwner, account Account, credentials auth.SubscriptionCredentials, holder refreshHolder, refreshErr error) (Account, error) {
	leaseID := holder.leaseID
	if errors.Is(refreshErr, errRefreshLeaseLost) {
		return Account{}, r.releaseRefreshLease(ctx, owner, account.ID, leaseID, errRefreshLeaseLost)
	}
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return Account{}, r.releaseRefreshLease(ctx, owner, account.ID, leaseID, refreshErr)
	}
	log := observability.FromContext(ctx).With("owner_id", owner.LogKey(), "account_id", account.ID, "provider", account.Provider,
		"refresh_lease_id", leaseID, "token_refresh_version", credentials.TokenRefreshVersion, "lease_taken_over", holder.tookOver)
	var oauthErr *OAuthRefreshError
	if errors.As(refreshErr, &oauthErr) {
		log = log.With("upstream_status", oauthErr.Status, "provider_error_code", oauthErr.Code, "provider_error_type", oauthErr.ErrorType, "provider_request_id", oauthErr.RequestID)
	}
	var terminal terminalRefreshError
	if errors.As(refreshErr, &terminal) && terminal.Terminal() {
		log.Warn("Subscription credential refresh rejected", "subscription_event", auth.SubscriptionCredentialEventRejected, "reason", refreshRejectionReason(refreshErr), "err", refreshErr)
		latest, loadErr := r.store.LoadSubscriptionCredentials(ctx, owner, account.ID)
		if loadErr == nil && (latest.TokenRefreshLeaseID != leaseID || latest.TokenRefreshVersion != credentials.TokenRefreshVersion || !bytes.Equal(latest.RefreshToken, credentials.RefreshToken)) {
			if subscriptionCredentialAvailable(ctx, account.Provider, latest, r.clock()) && subscriptionAccessTokenUsable(latest, r.clock()) {
				return applySubscriptionCredentials(account, latest), r.releaseRefreshLease(ctx, owner, account.ID, leaseID, nil)
			}
			return Account{}, r.releaseRefreshLease(ctx, owner, account.ID, leaseID, errRefreshLeaseLost)
		}
		if loadErr != nil {
			log.Error("Failed to reload subscription credentials after terminal refresh failure", "err", loadErr)
			refreshErr = errors.Join(refreshErr, loadErr)
		}
		if holder.tookOver {
			log.Warn("Subscription credential refresh rejected on a taken-over lease; not disabling", "err", refreshErr)
			return Account{}, r.releaseRefreshLease(ctx, owner, account.ID, leaseID, errRefreshLeaseLost)
		}
	} else {
		log.Warn("Subscription credential refresh failed", "err", refreshErr)
	}
	refreshErr = r.recordRefreshFailure(ctx, owner, account.ID, account.Provider, leaseID, credentials.TokenRefreshVersion, refreshErr)
	return Account{}, r.releaseRefreshLease(ctx, owner, account.ID, leaseID, refreshErr)
}

func (r *Runtime) releaseRefreshLease(ctx context.Context, owner auth.SubscriptionOwner, accountID, leaseID string, err error) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshReleaseTimeout)
	defer cancel()
	if releaseErr := r.store.ReleaseSubscriptionRefreshLease(releaseCtx, owner, accountID, leaseID); releaseErr != nil {
		observability.FromContext(ctx).Error("Failed to release subscription refresh lease", "owner_id", owner.LogKey(), "account_id", accountID, "err", releaseErr)
		return errors.Join(err, fmt.Errorf("release subscription refresh lease: %w", releaseErr))
	}
	return err
}

func subscriptionAccessTokenUsable(credentials auth.SubscriptionCredentials, now time.Time) bool {
	if len(credentials.AccessToken) == 0 {
		return false
	}
	return credentials.AccessTokenExpiresAt == nil || credentials.AccessTokenExpiresAt.IsZero() || credentials.AccessTokenExpiresAt.After(now.Add(time.Minute))
}

func subscriptionCredentialAvailable(ctx context.Context, provider Provider, credentials auth.SubscriptionCredentials, now time.Time) bool {
	var cooldownUntil time.Time
	if credentials.CooldownUntil != nil {
		cooldownUntil = *credentials.CooldownUntil
	}
	return credentials.Enabled && subscriptionAccountEligible(ctx, provider, credentials.State, cooldownUntil, now)
}

func applySubscriptionCredentials(account Account, credentials auth.SubscriptionCredentials) Account {
	account.AccessToken = string(credentials.AccessToken)
	account.TokenRefreshVersion = credentials.TokenRefreshVersion
	if credentials.AccessTokenExpiresAt == nil {
		account.AccessTokenExpiresAt = time.Time{}
	} else {
		account.AccessTokenExpiresAt = *credentials.AccessTokenExpiresAt
	}
	if credentials.CooldownUntil == nil {
		account.CooldownTil = time.Time{}
	} else {
		account.CooldownTil = *credentials.CooldownUntil
	}
	account.State = credentials.State
	return account
}

func waitForRefresh(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runtime) recordRefreshFailure(ctx context.Context, owner auth.SubscriptionOwner, accountID string, provider Provider, leaseID string, expectedVersion int64, refreshErr error) error {
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return refreshErr
	}
	var terminal interface{ Terminal() bool }
	if errors.As(refreshErr, &terminal) && terminal.Terminal() {
		disconnectLog := observability.FromContext(ctx).With("provider", provider, "account_id", accountID, "owner_id", owner.LogKey(),
			"refresh_lease_id", leaseID, "token_refresh_version", expectedVersion)
		var mismatch *providerAccountMismatchError
		if store, ok := r.store.(credentialRejectionStore); ok && !errors.As(refreshErr, &mismatch) {
			now := r.clock()
			reason := refreshRejectionReason(refreshErr)
			input := auth.SubscriptionCredentialRejectionInput{
				Reason: reason, RejectedAt: now, Window: credentialRejectionWindow, LeaseID: leaseID, ExpectedVersion: expectedVersion,
				RequestID: observability.RequestIDFromContext(ctx),
			}
			var oauthErr *OAuthRefreshError
			if errors.As(refreshErr, &oauthErr) {
				input.UpstreamStatus = oauthErr.Status
				input.ProviderErrorType = oauthErr.ErrorType
				input.ProviderRequestID = oauthErr.RequestID
			}
			rejection, err := store.RecordSubscriptionCredentialRejection(ctx, owner, accountID, input)
			if errors.Is(err, auth.ErrSubscriptionRefreshConflict) {
				return errRefreshLeaseLost
			}
			if err != nil {
				observability.FromContext(ctx).Error("Failed to record subscription credential rejection",
					"provider", provider, "account_id", accountID, "err", err)
				return errors.Join(refreshErr, fmt.Errorf("record subscription credential rejection: %w", err))
			}
			if !reconnectWarranted(rejection, reason, now) {
				cooldownUntil := now.Add(credentialRejectionCooldown(rejection.Count))
				observability.FromContext(ctx).Warn("Subscription credential refresh rejected; cooling down",
					"provider", provider, "account_id", accountID, "reason", reason, "strikes", rejection.Count,
					"first_rejected_at", rejection.FirstRejectedAt, "cooldown_until", cooldownUntil)
				if err := r.store.CooldownSubscriptionAccountIfRefreshHolder(ctx, owner, accountID, leaseID, expectedVersion, cooldownUntil); err != nil {
					if errors.Is(err, auth.ErrSubscriptionRefreshConflict) {
						return errRefreshLeaseLost
					}
					observability.FromContext(ctx).Error("Failed to persist subscription account cooldown after token rejection",
						"provider", provider, "account_id", accountID, "err", err)
					return errors.Join(refreshErr, fmt.Errorf("persist subscription account cooldown: %w", err))
				}
				return &credentialRejectionCooldownError{err: refreshErr, until: cooldownUntil}
			}
			disconnectLog = disconnectLog.With("reason", reason, "provider_error_code", reason, "strikes", rejection.Count,
				"first_rejected_at", rejection.FirstRejectedAt, "repeated_disconnect", rejection.RepeatedDisconnect,
				"provider_error_type", input.ProviderErrorType, "provider_request_id", input.ProviderRequestID, "upstream_status", input.UpstreamStatus)
		}
		if err := r.store.DisableSubscriptionAccountIfRefreshHolder(ctx, owner, accountID, leaseID, expectedVersion); err != nil {
			if errors.Is(err, auth.ErrSubscriptionRefreshConflict) {
				return errRefreshLeaseLost
			}
			observability.FromContext(ctx).Error("Failed to persist disabled subscription account after token rejection",
				"provider", provider, "account_id", accountID, "err", err)
			return errors.Join(refreshErr, fmt.Errorf("persist disabled subscription account state: %w", err))
		}
		disconnectLog.Warn("Subscription account needs reconnect after repeated refresh rejections", "subscription_event", auth.SubscriptionCredentialEventReconnectRequired, "err", refreshErr)
		return refreshErr
	}
	cooldownUntil := r.clock().Add(time.Minute)
	if err := r.store.CooldownSubscriptionAccountIfRefreshHolder(ctx, owner, accountID, leaseID, expectedVersion, cooldownUntil); err != nil {
		if errors.Is(err, auth.ErrSubscriptionRefreshConflict) {
			return errRefreshLeaseLost
		}
		observability.FromContext(ctx).Error("Failed to persist subscription account cooldown after token refresh failure",
			"provider", provider, "account_id", accountID, "err", err)
		return errors.Join(refreshErr, fmt.Errorf("persist subscription account cooldown: %w", err))
	}
	return refreshErr
}

type providerAccountMismatchError struct{}

func (*providerAccountMismatchError) Error() string {
	return "refreshed subscription account identity changed"
}
func (*providerAccountMismatchError) Terminal() bool { return true }

func (r *Runtime) admit(ctx context.Context, accountID string, owner auth.SubscriptionOwner) (auth.SubscriptionOwner, error) {
	store, ok := r.store.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	})
	if !ok {
		return auth.SubscriptionOwner{}, errors.New("subscription candidate admission is not configured")
	}
	accounts, err := store.ListSubscriptionCandidates(ctx, owner)
	if err != nil {
		return auth.SubscriptionOwner{}, err
	}
	for _, account := range accounts {
		if account.ID == accountID && account.SubscriberID != "" {
			return auth.SubscriptionOwner{SubscriberID: account.SubscriberID, APIKeyID: account.EnrolledByAPIKeyID, InstallationID: owner.InstallationID}, nil
		}
	}
	return auth.SubscriptionOwner{}, ErrNoAvailableAccount
}
