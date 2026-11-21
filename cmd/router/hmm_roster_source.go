package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

const hmmRosterTTL = 5 * time.Minute

const hmmRosterRetryBackoff = 30 * time.Second

type rosterFetcher interface {
	Roster(ctx context.Context) ([]string, error)
}

type activePolicySource interface {
	Active() *policyregistry.Snapshot
}

type hmmRosterSource struct {
	fetch        rosterFetcher
	active       activePolicySource
	fetchTimeout time.Duration

	group singleflight.Group

	mu              sync.Mutex
	cached          []router.DeployedEntry
	cachedPolicy    *policyregistry.Snapshot
	fetchedAt       time.Time
	haveCache       bool
	warnedRosterKey string
}

func newHMMRosterSource(fetch rosterFetcher, fetchTimeout time.Duration) *hmmRosterSource {
	if fetchTimeout <= 0 {
		fetchTimeout = policyclient.DefaultTimeout
	}
	active, _ := fetch.(activePolicySource)
	return &hmmRosterSource{fetch: fetch, active: active, fetchTimeout: fetchTimeout}
}

func (s *hmmRosterSource) activePolicy() *policyregistry.Snapshot {
	if s.active == nil {
		return nil
	}
	return s.active.Active()
}

func (s *hmmRosterSource) fresh(policy *policyregistry.Snapshot) bool {
	return s.haveCache && s.cachedPolicy == policy && time.Since(s.fetchedAt) < hmmRosterTTL
}

func (s *hmmRosterSource) HMMDeployedModels(ctx context.Context) (entries []router.DeployedEntry, err error) {
	policy := s.activePolicy()
	s.mu.Lock()
	if s.fresh(policy) {
		cached := cloneDeployedEntries(s.cached)
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	ch := s.group.DoChan("roster", func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.fetchTimeout)
		defer cancel()
		return s.refresh(fetchCtx)
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		entries, ok := res.Val.([]router.DeployedEntry)
		if !ok {
			return nil, fmt.Errorf("hmm roster refresh returned %T", res.Val)
		}
		return cloneDeployedEntries(entries), nil
	}
}

func (s *hmmRosterSource) refresh(ctx context.Context) ([]router.DeployedEntry, error) {
	policy := s.activePolicy()
	s.mu.Lock()
	if s.fresh(policy) {
		cached := cloneDeployedEntries(s.cached)
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	rosterIDs, fetchErr := s.fetch.Roster(ctx)
	if fetchErr != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.haveCache {
			s.cachedPolicy = policy
			s.fetchedAt = time.Now().Add(hmmRosterRetryBackoff - hmmRosterTTL)
			return cloneDeployedEntries(s.cached), nil
		}
		return nil, fetchErr
	}

	s.warnRosterDiagnostics(ctx, rosterIDs)
	mapped := hmm.DeployedEntriesSkippingUnknownArms(rosterIDs)
	s.mu.Lock()
	s.cached = mapped
	s.cachedPolicy = policy
	s.fetchedAt = time.Now()
	s.haveCache = true
	s.mu.Unlock()
	return mapped, nil
}

func (s *hmmRosterSource) warnRosterDiagnostics(ctx context.Context, rosterIDs []string) {
	diagnostics := hmm.ValidateRosterIDs(rosterIDs)
	key := ""
	if len(diagnostics) > 0 {
		key = strings.Join(rosterIDs, ",")
	}
	s.mu.Lock()
	repeat := key == s.warnedRosterKey
	s.warnedRosterKey = key
	s.mu.Unlock()
	if len(diagnostics) == 0 || repeat {
		return
	}
	invalid := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		invalid = append(invalid, d.RosterID+" ("+string(d.Reason)+")")
	}
	observability.FromContext(ctx).Warn("hmm roster arms failed catalog validation and cannot be routed", "invalid_arms", invalid, "invalid_count", len(invalid), "roster_size", len(rosterIDs))
}

func cloneDeployedEntries(in []router.DeployedEntry) []router.DeployedEntry {
	out := make([]router.DeployedEntry, len(in))
	copy(out, in)
	return out
}
