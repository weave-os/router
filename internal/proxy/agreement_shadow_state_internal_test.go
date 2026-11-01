package proxy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
)

var (
	shadowStateSol  = switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: agreementSol}, Group: escalation.High}
	shadowStateLuna = switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, Group: escalation.Low}
)

type memoryShadow struct {
	mu        sync.Mutex
	snapshots map[string]switchpolicy.ShadowSnapshot
	fail      error
	conflicts int
}

func shadowKey(key switchpolicy.ShadowKey) string {
	return key.InstallationID + ":" + string(key.SessionKey) + ":" + key.Role
}

func (m *memoryShadow) GetAgreementShadow(_ context.Context, key switchpolicy.ShadowKey) (switchpolicy.ShadowSnapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, found := m.snapshots[shadowKey(key)]
	return value, found, m.fail
}

func (m *memoryShadow) CompareAndSwapAgreementShadow(_ context.Context, key switchpolicy.ShadowKey, expected int64, next switchpolicy.ShadowSnapshot) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return false, m.fail
	}
	if m.conflicts > 0 {
		m.conflicts--
		return false, nil
	}
	if m.snapshots == nil {
		m.snapshots = make(map[string]switchpolicy.ShadowSnapshot)
	}
	if m.snapshots[shadowKey(key)].Revision != expected {
		return false, nil
	}
	m.snapshots[shadowKey(key)] = next
	return true, nil
}

func shadowObservation(second int) switchpolicy.ShadowObservation {
	return switchpolicy.ShadowObservation{RequestID: fmt.Sprintf("request-%d", second), StartedAt: time.Date(2026, 1, 1, 0, 0, second, 0, time.UTC),
		PolicyIdentity: "classifier/roster", Proposal: shadowStateSol, Confidence: .9, Eligible: []switchpolicy.Target{shadowStateSol.Target, shadowStateLuna.Target}, LivePin: shadowStateLuna}
}

func TestShadowVariantsDoNotShareVotesOrIncumbents(t *testing.T) {
	store := &memoryShadow{}
	key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
	first, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(1))
	if err != nil {
		t.Fatal(err)
	}
	if !first.SeededFromLivePin || first.OneUp.Next.Current != shadowStateSol || first.TwoUp.Next.Current != shadowStateLuna || first.TwoUp.Votes != 1 {
		t.Fatalf("simulations must independently evaluate the same evidence: %+v", first)
	}
	second, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(2))
	if err != nil {
		t.Fatal(err)
	}
	if second.OneUp.Switched || !second.TwoUp.Switched || second.TwoUp.Next.Current != shadowStateSol {
		t.Fatalf("two-up did not maintain its own agreement: %+v", second)
	}
	for vote := 1; vote <= 5; vote++ {
		observation := shadowObservation(vote + 2)
		observation.Proposal = shadowStateLuna
		out, err := advanceAgreementShadow(context.Background(), store, key, observation)
		if err != nil {
			t.Fatal(err)
		}
		if out.OneUp.Switched != (vote == 5) || out.TwoUp.Switched != (vote == 5) {
			t.Fatalf("downgrade vote %d: %+v", vote, out)
		}
	}
}

func TestShadowGapsInvalidateBothHistories(t *testing.T) {
	for _, gap := range []switchpolicy.ShadowGap{switchpolicy.ShadowGapUnsupported, switchpolicy.ShadowGapFailure, switchpolicy.ShadowGapMissing} {
		t.Run(string(gap), func(t *testing.T) {
			store := &memoryShadow{}
			key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
			initial, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(1))
			if err != nil {
				t.Fatal(err)
			}
			boundary := shadowObservation(2)
			boundary.Gap = gap
			out, err := advanceAgreementShadow(context.Background(), store, key, boundary)
			if err != nil {
				t.Fatal(err)
			}
			if out.Gap != gap || out.InvalidatedSegment != initial.SegmentID {
				t.Fatalf("gap not recorded: %+v", out)
			}
			state, _, _ := store.GetAgreementShadow(context.Background(), key)
			if state.OneUp.Current.Target != (switchpolicy.Target{}) || state.TwoUp.Current.Target != (switchpolicy.Target{}) {
				t.Fatalf("gap kept stale simulated choices: %+v", state)
			}
			fresh, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(3))
			if err != nil {
				t.Fatal(err)
			}
			if fresh.SegmentID == initial.SegmentID || fresh.TwoUp.Votes != 1 {
				t.Fatalf("new segment borrowed old votes: %+v", fresh)
			}
		})
	}
}

func TestShadowOrderDuplicatesAndEligibility(t *testing.T) {
	store := &memoryShadow{conflicts: 2}
	key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
	initial, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(2))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(2))
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate counted: %+v %v", duplicate, err)
	}
	late, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(1))
	if err != nil {
		t.Fatal(err)
	}
	if late.Gap != switchpolicy.ShadowGapOutOfOrder || late.InvalidatedSegment != initial.SegmentID {
		t.Fatalf("out-of-order completion was treated as consecutive evidence: %+v", late)
	}
	state, _, _ := store.GetAgreementShadow(context.Background(), key)
	if !state.LastStartedAt.Equal(shadowObservation(2).StartedAt) {
		t.Fatal("out-of-order completion moved the watermark backwards")
	}
	if _, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(3)); err != nil {
		t.Fatal(err)
	}
	observation := shadowObservation(4)
	observation.Proposal = shadowStateLuna
	observation.Eligible = []switchpolicy.Target{shadowStateLuna.Target}
	observation.Confidence = .2
	out, err := advanceAgreementShadow(context.Background(), store, key, observation)
	if err != nil {
		t.Fatal(err)
	}
	if out.OneUp.Override != switchpolicy.OverrideIncumbentIneligible || out.OneUp.Next.Current != shadowStateLuna {
		t.Fatalf("shadow held a now-ineligible target: %+v", out)
	}
}

func TestShadowNamespaceAndPolicyBoundaries(t *testing.T) {
	store := &memoryShadow{}
	key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
	first, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(1))
	if err != nil {
		t.Fatal(err)
	}
	other := key
	other.Role = "other-role"
	independent, err := advanceAgreementShadow(context.Background(), store, other, shadowObservation(2))
	if err != nil || independent.TwoUp.Votes != 1 {
		t.Fatalf("role reused votes: %+v %v", independent, err)
	}
	changed := shadowObservation(3)
	changed.PolicyIdentity = "new-classifier/roster"
	out, err := advanceAgreementShadow(context.Background(), store, key, changed)
	if err != nil {
		t.Fatal(err)
	}
	if out.Gap != switchpolicy.ShadowGapPolicyChanged || out.InvalidatedSegment != first.SegmentID || out.TwoUp.Votes != 1 {
		t.Fatalf("policy boundary reused old history: %+v", out)
	}
}

func TestShadowConcurrentDuplicateHasOneCommit(t *testing.T) {
	store := &memoryShadow{}
	key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
	var wg sync.WaitGroup
	outcomes := make(chan switchpolicy.ShadowOutcome, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(1))
			outcomes <- out
			errs <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	duplicates := 0
	for out := range outcomes {
		if out.Duplicate {
			duplicates++
		}
	}
	state, _, _ := store.GetAgreementShadow(context.Background(), key)
	if duplicates != 1 || state.Revision != 1 || state.TwoUp.Votes != 1 {
		t.Fatalf("duplicate race corrupted state: %+v duplicates=%d", state, duplicates)
	}
	store.fail = errors.New("unavailable")
	if _, err := advanceAgreementShadow(context.Background(), store, key, shadowObservation(2)); err == nil {
		t.Fatal("store failure lost its diagnostic")
	}
}

func TestShadowColdStartAndConflictExhaustion(t *testing.T) {
	key := switchpolicy.ShadowKey{InstallationID: "installation", SessionKey: []byte("thread"), Role: sessionpin.DefaultRole}
	event := shadowObservation(1)
	event.LivePin = switchpolicy.Selection{}
	store := &memoryShadow{}
	out, err := advanceAgreementShadow(context.Background(), store, key, event)
	if err != nil || !out.StartedSegment || out.SeededFromLivePin || out.OneUp.Reason != switchpolicy.ReasonInitial || out.TwoUp.Reason != switchpolicy.ReasonInitial {
		t.Fatalf("unavailable incumbent must be explicitly cold-started: %+v %v", out, err)
	}
	conflicting := &memoryShadow{conflicts: 20}
	if _, err := advanceAgreementShadow(context.Background(), conflicting, key, event); !errors.Is(err, errAgreementShadowConflict) {
		t.Fatalf("retry budget lost its diagnostic: %v", err)
	}
	if _, found, err := conflicting.GetAgreementShadow(context.Background(), key); err != nil || found {
		t.Fatalf("failed compare-and-swap published uncommitted votes: found=%v err=%v", found, err)
	}
}
