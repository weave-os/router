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
		keepTaskTools     bool
		want              bool
	}{
		{"Read", false, false, false},           // real tool: never stripped
		{"Read", true, false, false},            // real tool: never stripped
		{"Read", false, true, false},            // real tool: never stripped
		{"NotebookEdit", false, false, false},   // coding tool: never stripped
		{"ScheduleWakeup", false, false, false}, // scheduling: never stripped
		{"CronCreate", false, false, false},     // scheduling: never stripped
		{"CronDelete", false, false, false},     // scheduling: never stripped
		{"CronList", false, false, false},       // scheduling: never stripped
		{"Monitor", true, false, false},         // scheduling: never stripped
		{"BashOutput", false, false, false},     // shell session: never stripped
		{"KillShell", true, false, false},       // shell session: never stripped
		{"Task", false, false, true},            // orchestration: stripped when flag off
		{"Task", false, true, true},             // task-tool flag alone keeps nothing
		{"Task", true, false, false},            // orchestration: kept when flag on
		{"Agent", true, false, false},           // orchestration (current CC name): kept when flag on
		{"TaskOutput", true, false, false},      // background-agent output: kept when flag on
		{"TaskStop", true, false, false},        // background-agent stop: kept when flag on
		{"TaskCreate", true, false, true},       // task-list bookkeeping: stripped with only orchestration on
		{"TaskUpdate", true, false, true},       // task-list bookkeeping: stripped with only orchestration on
		{"TaskGet", true, false, true},          // task-list bookkeeping: stripped with only orchestration on
		{"TaskList", true, false, true},         // task-list bookkeeping: stripped with only orchestration on
		{"TaskCreate", true, true, false},       // task-list bookkeeping: kept when both flags on
		{"TaskUpdate", true, true, false},       // task-list bookkeeping: kept when both flags on
		{"TaskGet", true, true, false},          // task-list bookkeeping: kept when both flags on
		{"TaskList", true, true, false},         // task-list bookkeeping: kept when both flags on
		{"TaskCreate", false, true, true},       // task tools stay nested under orchestration
		{"TaskList", false, true, true},         // task tools stay nested under orchestration
		{"Workflow", true, false, false},        // orchestration: kept when flag on
		{"ExitPlanMode", true, false, false},    // orchestration: kept when flag on
		{"UpdatePlan", true, false, false},      // orchestration: kept when flag on
		{"AskUserQuestion", true, true, true},   // CC-only non-orchestration: stripped even when both flags on
		{"ToolSearch", false, false, false},     // deferred MCP loader: always kept
		{"ToolSearch", true, false, false},      // independent of the orchestration flag
		{"TodoWrite", false, false, true},       // CC-only non-orchestration: stripped
		{"TodoWrite", true, true, true},         // TodoWrite is not one of the four task tools
		{"SendMessage", true, true, true},       // CC-only subagent messaging: stripped even when both flags on
	}
	for _, tc := range cases {
		opts := ccToolFilterOptions{KeepOrchestration: tc.keepOrchestration, KeepTaskTools: tc.keepTaskTools}
		if got := shouldStripCCTool(tc.name, opts); got != tc.want {
			t.Errorf("shouldStripCCTool(%q, %+v) = %v, want %v", tc.name, opts, got, tc.want)
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
