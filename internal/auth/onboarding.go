package auth

import "time"

// OnboardingObserver receives committed onboarding transitions. Implementations
// must be nonblocking, concurrency-safe, and best-effort; delivery is not durable.
type OnboardingObserver interface {
	APIKeyFirstUsed(APIKeyFirstUsedEvent)
	SubscriptionConnected(SubscriptionConnectedEvent)
	HarnessLifecycle(HarnessLifecycleEvent)
}

// APIKeyFirstUsedEvent records a routing key's first successful authentication,
// including validation probes. It does not imply a successful inference.
type APIKeyFirstUsedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	Harness                string
	OccurredAt             time.Time
}

// SubscriptionConnectedEvent records a newly enrolled or adopted account.
type SubscriptionConnectedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	AccountID              string
	Provider               SubscriptionProvider
	OccurredAt             time.Time
}

// HarnessLifecycleAction is a client-side state change a harness CLI or its
// local proxy reports after it has already happened.
type HarnessLifecycleAction string

const (
	HarnessLifecycleActionOff       HarnessLifecycleAction = "off"
	HarnessLifecycleActionOn        HarnessLifecycleAction = "on"
	HarnessLifecycleActionUninstall HarnessLifecycleAction = "uninstall"
	// HarnessLifecycleActionConnected reports the harness's first request the
	// router served, for harnesses whose key is shared with another harness
	// (Claude Desktop reuses the Claude Code key), so key first use can't say.
	HarnessLifecycleActionConnected HarnessLifecycleAction = "connected"
)

// Valid reports whether the action is one the CLI is allowed to report.
func (a HarnessLifecycleAction) Valid() bool {
	switch a {
	case HarnessLifecycleActionOff, HarnessLifecycleActionOn, HarnessLifecycleActionUninstall, HarnessLifecycleActionConnected:
		return true
	}
	return false
}

// LifecycleHarness names the client whose local routing config changed.
// Values match the harness ids stored on minted keys (APIKey.Harness), so
// lifecycle spans join the harness_connected ones on the same string.
type LifecycleHarness string

const (
	LifecycleHarnessClaude   LifecycleHarness = "claude_code"
	LifecycleHarnessCodex    LifecycleHarness = "codex"
	LifecycleHarnessOpencode LifecycleHarness = "opencode"
	LifecycleHarnessPi       LifecycleHarness = "pi"
	LifecycleHarnessCopilot  LifecycleHarness = "copilot"
	LifecycleHarnessDesktop  LifecycleHarness = "claude_desktop"
)

// Valid reports whether the harness is one the installer configures.
func (h LifecycleHarness) Valid() bool {
	switch h {
	case LifecycleHarnessClaude, LifecycleHarnessCodex, LifecycleHarnessOpencode, LifecycleHarnessPi,
		LifecycleHarnessCopilot, LifecycleHarnessDesktop:
		return true
	}
	return false
}

// HarnessLifecycleEvent records a client-reported off/on/uninstall/connected
// of a harness's local router config. The local mutation has already happened; the
// report is fail-open on the client, so absence of an event proves nothing.
type HarnessLifecycleEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	Harness                LifecycleHarness
	Action                 HarnessLifecycleAction
	OccurredAt             time.Time
}

// ReportHarnessLifecycle forwards a client-reported toggle to the observer.
// Callers have already authenticated the key; the service only stamps the
// clock and skips delivery when no observer is wired.
func (s *Service) ReportHarnessLifecycle(installation *Installation, apiKey *APIKey, harness LifecycleHarness, action HarnessLifecycleAction) {
	if s.onboarding == nil || installation == nil || apiKey == nil {
		return
	}
	s.onboarding.HarnessLifecycle(HarnessLifecycleEvent{
		InstallationExternalID: installation.ExternalID,
		CredentialSubjectID:    apiKey.CredentialSubjectID,
		APIKeyID:               apiKey.ID,
		Harness:                harness,
		Action:                 action,
		OccurredAt:             s.now(),
	})
}
