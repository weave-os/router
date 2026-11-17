package auth

import "time"

type OnboardingObserver interface {
	APIKeyFirstUsed(APIKeyFirstUsedEvent)
	SubscriptionConnected(SubscriptionConnectedEvent)
	HarnessLifecycle(HarnessLifecycleEvent)
}

type APIKeyFirstUsedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	Harness                string
	OccurredAt             time.Time
}

type SubscriptionConnectedEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	AccountID              string
	Provider               SubscriptionProvider
	OccurredAt             time.Time
}

type HarnessLifecycleAction string

const (
	HarnessLifecycleActionOff       HarnessLifecycleAction = "off"
	HarnessLifecycleActionOn        HarnessLifecycleAction = "on"
	HarnessLifecycleActionUninstall HarnessLifecycleAction = "uninstall"
	HarnessLifecycleActionConnected HarnessLifecycleAction = "connected"
)

func (a HarnessLifecycleAction) Valid() bool {
	switch a {
	case HarnessLifecycleActionOff, HarnessLifecycleActionOn, HarnessLifecycleActionUninstall, HarnessLifecycleActionConnected:
		return true
	}
	return false
}

type LifecycleHarness string

const (
	LifecycleHarnessClaude   LifecycleHarness = "claude_code"
	LifecycleHarnessCodex    LifecycleHarness = "codex"
	LifecycleHarnessOpencode LifecycleHarness = "opencode"
	LifecycleHarnessPi       LifecycleHarness = "pi"
	LifecycleHarnessCopilot  LifecycleHarness = "copilot"
	LifecycleHarnessDesktop  LifecycleHarness = "claude_desktop"
)

func (h LifecycleHarness) Valid() bool {
	switch h {
	case LifecycleHarnessClaude, LifecycleHarnessCodex, LifecycleHarnessOpencode, LifecycleHarnessPi,
		LifecycleHarnessCopilot, LifecycleHarnessDesktop:
		return true
	}
	return false
}

type HarnessLifecycleEvent struct {
	InstallationExternalID string
	CredentialSubjectID    string
	APIKeyID               string
	Harness                LifecycleHarness
	Action                 HarnessLifecycleAction
	OccurredAt             time.Time
}

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
