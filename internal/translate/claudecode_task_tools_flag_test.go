package translate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// claudeCodeTaskToolBody carries one ordinary tool, one orchestration tool,
// the four task-list tools, and both reminder shapes Claude Code emits: a
// standalone text block on a user turn and a role:"system" turn.
const claudeCodeTaskToolBody = `{
	"model":"claude-opus-4-7",
	"system":"You are Claude Code.",
	"messages":[
		{"role":"user","content":[
			{"type":"text","text":"fix the bug"},
			{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently. Consider updating task status.\n</system-reminder>"}
		]},
		{"role":"assistant","content":[{"type":"text","text":"on it"}]},
		{"role":"system","content":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"},
		{"role":"user","content":"continue"}
	],
	"tools":[
		{"name":"Read","description":"r","input_schema":{"type":"object"}},
		{"name":"Task","description":"sub-agent dispatch","input_schema":{"type":"object"}},
		{"name":"TaskCreate","description":"","input_schema":{"type":"object"}},
		{"name":"TaskUpdate","description":"","input_schema":{"type":"object"}},
		{"name":"TaskGet","description":"","input_schema":{"type":"object"}},
		{"name":"TaskList","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

func TestCCTaskToolsCrossVendor_FilterMatrix(t *testing.T) {
	cases := []struct {
		name              string
		keepOrchestration bool
		keepTaskTools     bool
		wantTools         []string
		wantToolsRemoved  int
		wantRemindersGone int
	}{
		{
			name:              "both off strips orchestration and task tools",
			wantTools:         []string{"Read"},
			wantToolsRemoved:  5,
			wantRemindersGone: 2,
		},
		{
			name:              "task tools alone keep nothing",
			keepTaskTools:     true,
			wantTools:         []string{"Read"},
			wantToolsRemoved:  5,
			wantRemindersGone: 2,
		},
		{
			name:              "orchestration alone keeps Task only",
			keepOrchestration: true,
			wantTools:         []string{"Read", "Task"},
			wantToolsRemoved:  4,
			wantRemindersGone: 2,
		},
		{
			name:              "both on keep the task scaffold and its reminders",
			keepOrchestration: true,
			keepTaskTools:     true,
			wantTools:         []string{"Read", "Task", "TaskCreate", "TaskUpdate", "TaskGet", "TaskList"},
			wantToolsRemoved:  0,
			wantRemindersGone: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(claudeCodeTaskToolBody))
			require.NoError(t, err)

			out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
				TargetModel:                       "deepseek/deepseek-v4-pro",
				KeepCrossVendorOrchestrationTools: tc.keepOrchestration,
				KeepCrossVendorTaskTools:          tc.keepTaskTools,
			})
			require.NoError(t, err)

			assert.ElementsMatch(t, tc.wantTools, emittedToolNames(t, out.Body))
			assert.Equal(t, tc.wantToolsRemoved, out.Stats.CCOnlyToolsStripped)
			assert.Equal(t, tc.wantRemindersGone, out.Stats.CCTaskRemindersStripped)

			gotReminders := strings.Count(string(out.Body), "task tools haven")
			wantReminders := 2 - tc.wantRemindersGone
			assert.Equal(t, wantReminders, gotReminders, "reminders survive exactly when the task tools do")
		})
	}
}

// The flag defaults off, so an emit that only sets the orchestration flag must
// be byte-identical to one that leaves the new option at its zero value.
func TestCCTaskToolsCrossVendor_OffIsByteIdenticalToPreFlagBehaviour(t *testing.T) {
	for _, keepOrchestration := range []bool{false, true} {
		env, err := translate.ParseAnthropic([]byte(claudeCodeTaskToolBody))
		require.NoError(t, err)
		base, err := env.PrepareOpenAI(nil, translate.EmitOptions{
			TargetModel:                       "deepseek/deepseek-v4-pro",
			KeepCrossVendorOrchestrationTools: keepOrchestration,
		})
		require.NoError(t, err)

		explicitOff, err := env.PrepareOpenAI(nil, translate.EmitOptions{
			TargetModel:                       "deepseek/deepseek-v4-pro",
			KeepCrossVendorOrchestrationTools: keepOrchestration,
			KeepCrossVendorTaskTools:          false,
		})
		require.NoError(t, err)

		assert.Equal(t, string(base.Body), string(explicitOff.Body))
		assert.Equal(t, base.Stats.CCOnlyToolsStripped, explicitOff.Stats.CCOnlyToolsStripped)
		assert.Equal(t, base.Stats.CCTaskRemindersStripped, explicitOff.Stats.CCTaskRemindersStripped)
	}
}

// Native Anthropic targets never run the cross-vendor filter: every tool and
// both reminders survive regardless of the flags.
func TestCCTaskToolsCrossVendor_AnthropicTargetUnaffected(t *testing.T) {
	for _, keepTaskTools := range []bool{false, true} {
		env, err := translate.ParseAnthropic([]byte(claudeCodeTaskToolBody))
		require.NoError(t, err)

		out, err := env.PrepareAnthropic(nil, translate.EmitOptions{
			TargetModel:                       "claude-opus-4-7",
			KeepCrossVendorOrchestrationTools: false,
			KeepCrossVendorTaskTools:          keepTaskTools,
		})
		require.NoError(t, err)

		body := string(out.Body)
		for _, name := range []string{"Read", "Task", "TaskCreate", "TaskUpdate", "TaskGet", "TaskList"} {
			assert.Contains(t, body, `"name":"`+name+`"`)
		}
		assert.Equal(t, 2, strings.Count(body, "task tools haven"))
		assert.Zero(t, out.Stats.CCOnlyToolsStripped)
		assert.Zero(t, out.Stats.CCTaskRemindersStripped)
	}
}
