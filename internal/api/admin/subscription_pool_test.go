package admin

import "testing"

func TestSubscriptionLimitWindowsClaudeReadsSessionAndWeekly(t *testing.T) {
	windows := subscriptionLimitWindows(subscriptionProviderClaude, map[string]string{
		claudeWeeklyUtilizationHeader:  "0.4",
		claudeWeeklyResetHeader:        "1793900000",
		claudeSessionUtilizationHeader: "1.7",
	}, nil)

	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want session and weekly", windows)
	}
	if windows[0].WindowMinutes != sessionLimitWindowMinutes || windows[0].UsedFraction != 1 || windows[0].ResetsAt != nil {
		t.Fatalf("session window = %+v, want clamped to 1 with no reset", windows[0])
	}
	if windows[1].WindowMinutes != weeklyLimitWindowMinutes || windows[1].UsedFraction != 0.4 || windows[1].ResetsAt == nil || *windows[1].ResetsAt != "2026-11-05T17:33:20Z" {
		t.Fatalf("weekly window = %+v", windows[1])
	}
}

func TestSubscriptionLimitWindowsCodexDropsZeroMinuteWindow(t *testing.T) {
	windows := subscriptionLimitWindows(subscriptionProviderCodex, nil, map[string]string{
		codexPrimaryUsedPercentHeader:     "12",
		codexPrimaryWindowMinutesHeader:   "0",
		codexSecondaryUsedPercentHeader:   "35",
		codexSecondaryWindowMinutesHeader: "10080",
	})

	if len(windows) != 1 || windows[0].WindowMinutes != weeklyLimitWindowMinutes || windows[0].UsedFraction != 0.35 {
		t.Fatalf("windows = %+v, want only the weekly window at 35%%", windows)
	}
}

func TestSubscriptionLimitWindowsIgnoresOtherProvidersHeaders(t *testing.T) {
	windows := subscriptionLimitWindows(subscriptionProviderCodex, map[string]string{claudeWeeklyUtilizationHeader: "0.5"}, nil)
	if len(windows) != 0 {
		t.Fatalf("windows = %+v, want none", windows)
	}
}
