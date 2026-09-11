package translate

import "testing"

// claudeCodeOrchestrationToolNames must be a strict subset of claudeCodeOnlyToolNames;
// otherwise shouldStripCCTool silently skips the keep-orchestration branch for any
// name missing from the CC-only set.
func TestOrchestrationToolsAreSubsetOfCCOnly(t *testing.T) {
	for name := range claudeCodeOrchestrationToolNames {
		if !isClaudeCodeOnlyTool(name) {
			t.Errorf("orchestration tool %q is not in claudeCodeOnlyToolNames; the subset invariant is broken", name)
		}
	}
}

func TestAlwaysKeptToolsAreSubsetOfCCOnly(t *testing.T) {
	for name := range claudeCodeAlwaysKeptToolNames {
		if !isClaudeCodeOnlyTool(name) {
			t.Errorf("always-kept tool %q is not in claudeCodeOnlyToolNames; the subset invariant is broken", name)
		}
	}
}

func TestTaskBookkeepingToolsAreCCOnlyAndNotOrchestration(t *testing.T) {
	for name := range claudeCodeTaskBookkeepingToolNames {
		if !isClaudeCodeOnlyTool(name) {
			t.Errorf("bookkeeping tool %q is not in claudeCodeOnlyToolNames", name)
		}
		if isCrossVendorOrchestrationTool(name) {
			t.Errorf("bookkeeping tool %q is in claudeCodeOrchestrationToolNames; it would survive cross-vendor emit", name)
		}
	}
}

func TestShouldStripCCTool(t *testing.T) {
	cases := []struct {
		name              string
		keepOrchestration bool
		want              bool
	}{
		{"Read", false, false},           // real tool: never stripped
		{"Read", true, false},            // real tool: never stripped
		{"NotebookEdit", false, false},   // coding tool: never stripped
		{"ScheduleWakeup", false, false}, // scheduling: never stripped
		{"CronCreate", false, false},     // scheduling: never stripped
		{"CronDelete", false, false},     // scheduling: never stripped
		{"CronList", false, false},       // scheduling: never stripped
		{"Monitor", true, false},         // scheduling: never stripped
		{"BashOutput", false, false},     // shell session: never stripped
		{"KillShell", true, false},       // shell session: never stripped
		{"Task", false, true},            // orchestration: stripped when flag off
		{"Task", true, false},            // orchestration: kept when flag on
		{"Agent", true, false},           // orchestration (current CC name): kept when flag on
		{"TaskOutput", true, false},      // background-agent output: kept when flag on
		{"TaskStop", true, false},        // background-agent stop: kept when flag on
		{"TaskCreate", true, true},       // task-list bookkeeping: stripped even when flag on
		{"TaskUpdate", true, true},       // task-list bookkeeping: stripped even when flag on
		{"TaskGet", true, true},          // task-list bookkeeping: stripped even when flag on
		{"TaskList", true, true},         // task-list bookkeeping: stripped even when flag on
		{"Workflow", true, false},        // orchestration: kept when flag on
		{"ExitPlanMode", true, false},    // orchestration: kept when flag on
		{"UpdatePlan", true, false},      // orchestration: kept when flag on
		{"AskUserQuestion", true, true},  // CC-only non-orchestration: stripped even when flag on
		{"ToolSearch", false, false},     // deferred MCP loader: always kept
		{"ToolSearch", true, false},      // independent of the orchestration flag
		{"TodoWrite", false, true},       // CC-only non-orchestration: stripped
		{"SendMessage", true, true},      // CC-only subagent messaging: stripped even when flag on
	}
	for _, tc := range cases {
		if got := shouldStripCCTool(tc.name, tc.keepOrchestration); got != tc.want {
			t.Errorf("shouldStripCCTool(%q, keep=%v) = %v, want %v", tc.name, tc.keepOrchestration, got, tc.want)
		}
	}
}

func TestRemoveTaskToolReminders(t *testing.T) {
	const rem = "<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"
	cases := []struct {
		in, want string
		n        int
	}{
		{"output\n\n" + rem, "output", 1},
		{rem, "", 1},
		{"a\n" + rem + "\n\nb\n" + rem, "a\n\nb", 2},
		{"<system-reminder>\nunrelated\n</system-reminder>\n" + rem, "<system-reminder>\nunrelated\n</system-reminder>", 1},
		{"the task tools haven't been used recently by anyone", "the task tools haven't been used recently by anyone", 0},
		{"<system-reminder>\nThe task tools haven't been used recently.", "<system-reminder>\nThe task tools haven't been used recently.", 0},
	}
	for _, c := range cases {
		got, n := removeTaskToolReminders(c.in)
		if got != c.want || n != c.n {
			t.Errorf("removeTaskToolReminders(%q) = (%q, %d), want (%q, %d)", c.in, got, n, c.want, c.n)
		}
	}
}
