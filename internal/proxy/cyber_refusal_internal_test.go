package proxy

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry/respobserve"
)

// repinFakeStore is a minimal sessionpin.Store for the re-pin unit tests.
type repinFakeStore struct {
	getPin  sessionpin.Pin
	hasPin  bool
	upserts []sessionpin.Pin
}

func (f *repinFakeStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	if !f.hasPin {
		return sessionpin.Pin{}, false, nil
	}
	p := f.getPin
	p.SessionKey = key
	p.Role = role
	return p, true, nil
}
func (f *repinFakeStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	f.upserts = append(f.upserts, p)
	return nil
}
func (f *repinFakeStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}
func (f *repinFakeStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (f *repinFakeStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (f *repinFakeStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (f *repinFakeStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*repinFakeStore) ExpireAndDemoteModel(context.Context, sessionpin.Pin, string, sessionpin.DemotionReason) error {
	return nil
}

func (f *repinFakeStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}
func (f *repinFakeStore) Consume(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}
func (f *repinFakeStore) SweepExpired(context.Context) error { return nil }

func repinCtx() context.Context {
	return context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, uuid.New().String())
}

func TestMaybeRepinOnRefusal_RepinsToFallbackModel(t *testing.T) {
	store := &repinFakeStore{} // no existing pin -> no PairedModel
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
	obs := &respobserve.RefusalObserver{Refused: true}
	served := router.Decision{Provider: "anthropic", Model: "claude-opus-4-8"}

	s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), obs, [sessionpin.SessionKeyLen]byte{1, 2, 3}, "main_loop", served)

	if len(store.upserts) != 1 {
		t.Fatalf("expected exactly 1 re-pin upsert, got %d", len(store.upserts))
	}
	got := store.upserts[0]
	if got.Model != "claude-sonnet-5" {
		t.Fatalf("re-pinned to %q, want claude-sonnet-5", got.Model)
	}
	if got.Provider == "" {
		t.Fatal("re-pin has no provider (catalog resolution failed)")
	}
	if got.Reason != "cyber-refusal-repin" {
		t.Fatalf("re-pin reason = %q, want cyber-refusal-repin", got.Reason)
	}
}

func TestMaybeRepinOnRefusal_PrefersPairedModel(t *testing.T) {
	store := &repinFakeStore{
		hasPin: true,
		getPin: sessionpin.Pin{PairedModel: "claude-haiku-4-5", PairedProvider: "anthropic"},
	}
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
	obs := &respobserve.RefusalObserver{Refused: true}
	served := router.Decision{Provider: "anthropic", Model: "claude-opus-4-8"}

	s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), obs, [sessionpin.SessionKeyLen]byte{4, 5, 6}, "main_loop", served)

	if len(store.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(store.upserts))
	}
	if got := store.upserts[0].Model; got != "claude-haiku-4-5" {
		t.Fatalf("re-pinned to %q, want the pin's PairedModel claude-haiku-4-5", got)
	}
	if got := store.upserts[0].Provider; got != "anthropic" {
		t.Fatalf("re-pin provider = %q, want anthropic (from PairedProvider)", got)
	}
}

// OpenAI's classifier declines the request on any of its models, so the pin's
// runner-up is only an escape when it is another vendor.
func TestRepinOffRefusingModel_SkipsPairedModelOnTheRefusingVendor(t *testing.T) {
	store := &repinFakeStore{
		hasPin: true,
		getPin: sessionpin.Pin{PairedModel: "gpt-5.4", PairedProvider: providers.ProviderOpenAI},
	}
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
	served := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol"}

	s.repinOffRefusingModel(repinCtx(), router.StrategyFromContext(repinCtx()), [sessionpin.SessionKeyLen]byte{8, 9}, "main_loop", served,
		providers.CyberPolicyErrorCode, providers.ProviderOpenAI)

	if len(store.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(store.upserts))
	}
	if got := store.upserts[0].Model; got != "claude-sonnet-5" {
		t.Fatalf("re-pinned to %q, want the off-vendor fallback claude-sonnet-5", got)
	}
	if got := store.upserts[0].Provider; got != providers.ProviderAnthropic {
		t.Fatalf("re-pin provider = %q, want anthropic", got)
	}
}

func TestMaybeRepinOnRefusal_NoOpCases(t *testing.T) {
	served := router.Decision{Provider: "anthropic", Model: "claude-opus-4-8"}
	key := [sessionpin.SessionKeyLen]byte{7}

	t.Run("nil observer", func(t *testing.T) {
		store := &repinFakeStore{}
		s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
		s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), nil, key, "main_loop", served)
		if len(store.upserts) != 0 {
			t.Fatalf("nil observer should not re-pin, got %d upserts", len(store.upserts))
		}
	})

	t.Run("no refusal observed", func(t *testing.T) {
		store := &repinFakeStore{}
		s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
		s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), &respobserve.RefusalObserver{Refused: false}, key, "main_loop", served)
		if len(store.upserts) != 0 {
			t.Fatalf("no refusal should not re-pin, got %d upserts", len(store.upserts))
		}
	})

	t.Run("re-pin disabled", func(t *testing.T) {
		store := &repinFakeStore{}
		s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: false, CyberRefusalFallbackModel: "claude-sonnet-5"}}
		s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), &respobserve.RefusalObserver{Refused: true}, key, "main_loop", served)
		if len(store.upserts) != 0 {
			t.Fatalf("re-pin disabled should not upsert, got %d", len(store.upserts))
		}
	})

	t.Run("served model already the fallback", func(t *testing.T) {
		store := &repinFakeStore{}
		s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
		alreadyFallback := router.Decision{Provider: "anthropic", Model: "claude-sonnet-5"}
		s.maybeRepinOnRefusal(repinCtx(), router.StrategyFromContext(repinCtx()), &respobserve.RefusalObserver{Refused: true}, key, "main_loop", alreadyFallback)
		if len(store.upserts) != 0 {
			t.Fatalf("re-pin to the same model should be skipped, got %d upserts", len(store.upserts))
		}
	})
}
