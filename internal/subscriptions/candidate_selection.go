package subscriptions

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

const candidateExplorationInterval = 30 * time.Second

type candidateSelectionReason string

const (
	candidateSelectionRanked candidateSelectionReason = "quota_and_load"
	candidateSelectionProbe  candidateSelectionReason = "bounded_exploration"
)

type candidateLoad struct {
	inFlight     int
	lastSelected time.Time
	sequence     uint64
}

type explorationBudget struct {
	selections int
	lastProbe  time.Time
}

type candidateComparison struct {
	AccountID    string                `json:"account_id"`
	Tier         auth.SubscriptionTier `json:"tier"`
	Observed     bool                  `json:"observed"`
	Exhausted    bool                  `json:"exhausted"`
	Billable     bool                  `json:"billable"`
	UsedFraction float64               `json:"used_fraction"`
	ResetAt      time.Time             `json:"reset_at"`
	ObservedAt   time.Time             `json:"observed_at"`
	InFlight     int                   `json:"in_flight"`
	LastSelected time.Time             `json:"last_selected"`
}

// Ranking and reserving share one lock so concurrent callers see work that is
// still refreshing credentials, as well as requests already sent upstream.
func (r *Runtime) reserveCandidate(ctx context.Context, accounts []*auth.SubscriptionAccount, requester auth.SubscriptionOwner, provider Provider, preferred string, signals map[string]CandidateSignal) (*auth.SubscriptionAccount, func()) {
	r.selectionMu.Lock()
	now := r.clock()
	r.selectionSequence++
	if r.selectionSequence%256 == 0 {
		for id, load := range r.candidateLoads {
			if load.inFlight == 0 && now.Sub(load.lastSelected) > 30*time.Minute {
				delete(r.candidateLoads, id)
			}
		}
	}
	for _, account := range accounts {
		if r.candidateLoads[account.ID] == nil {
			r.candidateLoads[account.ID] = &candidateLoad{}
		}
	}
	slices.SortStableFunc(accounts, func(left, right *auth.SubscriptionAccount) int {
		if left.ID == right.ID {
			return 0
		}
		leftPersonal := left.SubscriberID == requester.SubscriberID
		rightPersonal := right.SubscriberID == requester.SubscriberID
		if leftPersonal != rightPersonal {
			if leftPersonal {
				return -1
			}
			return 1
		}
		if signals != nil {
			leftSignal, rightSignal := signals[left.ID], signals[right.ID]
			if rank := candidateSignalRank(leftSignal) - candidateSignalRank(rightSignal); rank != 0 {
				return rank
			}
			leftLoad, rightLoad := r.candidateLoads[left.ID], r.candidateLoads[right.ID]
			if rank := cmp.Compare(leftLoad.inFlight, rightLoad.inFlight); rank != 0 {
				return rank
			}
			if leftSignal.ResetAt.IsZero() != rightSignal.ResetAt.IsZero() {
				if leftSignal.ResetAt.IsZero() {
					return 1
				}
				return -1
			}
			if rank := leftSignal.ResetAt.Compare(rightSignal.ResetAt); rank != 0 {
				return rank
			}
			if rank := cmp.Compare(leftSignal.UsedFraction, rightSignal.UsedFraction); rank != 0 {
				return rank
			}
			// Unknown seats have no expiry signal to distinguish them. Rotate
			// them even for sticky sessions until responses establish evidence.
			if !leftSignal.Observed {
				if rank := cmp.Compare(leftLoad.sequence, rightLoad.sequence); rank != 0 {
					return rank
				}
			}
		}
		if left.ID == preferred {
			return -1
		}
		if right.ID == preferred {
			return 1
		}
		return 0
	})
	reason := candidateSelectionRanked
	if signals != nil {
		scope := requester.InstallationID + "|" + requester.SubscriberID + "|" + string(provider)
		budget, ok := r.explorationBudgets.Get(scope)
		if !ok {
			budget = &explorationBudget{}
			r.explorationBudgets.Add(scope, budget)
		}
		if budget.selections >= candidateExplorationEvery-1 && now.Sub(budget.lastProbe) >= candidateExplorationInterval {
			probe := -1
			for i, account := range accounts {
				load, signal := r.candidateLoads[account.ID], signals[account.ID]
				if i == 0 || (account.SubscriberID == requester.SubscriberID) != (accounts[0].SubscriberID == requester.SubscriberID) ||
					signal.Exhausted || signal.Billable || account.State == auth.SubscriptionAccountStateExhausted || load.inFlight > 0 ||
					!load.lastSelected.IsZero() && now.Sub(load.lastSelected) < candidateExplorationIdle {
					continue
				}
				if probe < 0 || load.lastSelected.Before(r.candidateLoads[accounts[probe].ID].lastSelected) {
					probe = i
				}
			}
			if probe >= 0 {
				accounts[0], accounts[probe] = accounts[probe], accounts[0]
				budget.selections, budget.lastProbe = 0, now
				reason = candidateSelectionProbe
			}
		}
		if reason != candidateSelectionProbe {
			budget.selections = min(budget.selections+1, candidateExplorationEvery-1)
		}
	}
	comparisons := make([]candidateComparison, 0, min(len(accounts), 16))
	for _, account := range accounts[:min(len(accounts), 16)] {
		load, signal := r.candidateLoads[account.ID], signals[account.ID]
		comparisons = append(comparisons, candidateComparison{AccountID: account.ID, Tier: account.Tier,
			Observed: signal.Observed, Exhausted: signal.Exhausted, Billable: signal.Billable,
			UsedFraction: signal.UsedFraction, ResetAt: signal.ResetAt, ObservedAt: signal.ObservedAt,
			InFlight: load.inFlight, LastSelected: load.lastSelected})
	}
	selected := accounts[0]
	load := r.candidateLoads[selected.ID]
	load.inFlight++
	load.lastSelected, load.sequence = now, r.selectionSequence
	r.selectionMu.Unlock()
	observability.FromContext(ctx).Info("Subscription candidates evaluated",
		"subscription_event", auth.SubscriptionCredentialEventCandidateEvaluated, "account_id", selected.ID,
		"provider", provider, "selection_reason", reason, "candidate_count", len(accounts),
		"candidates", comparisons, "candidates_truncated", len(accounts) > len(comparisons))
	var once sync.Once
	return selected, func() {
		once.Do(func() {
			r.selectionMu.Lock()
			load.inFlight--
			r.selectionMu.Unlock()
		})
	}
}
