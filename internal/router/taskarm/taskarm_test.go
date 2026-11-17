package taskarm_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/taskarm"
)

func user(text string) router.ConversationMessage {
	return router.ConversationMessage{Role: "user", Text: text}
}

func assistant(text string) router.ConversationMessage {
	return router.ConversationMessage{Role: "assistant", Text: text}
}

func toolResult() router.ConversationMessage {
	return router.ConversationMessage{Role: "user", ToolResults: []router.ConversationToolResult{{Text: "ok"}}}
}

func toolCall() router.ConversationMessage {
	return router.ConversationMessage{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "run"}}}
}

func TestPreviousTurnLabel(t *testing.T) {
	const full = "Tests pass.\n@cycle export csv-writer verify running | Waiting on the slow suite."
	want := taskarm.Label{Goal: "export", Task: "csv-writer", Step: "verify"}
	cases := []struct {
		name     string
		messages []router.ConversationMessage
		want     taskarm.Label
		found    bool
	}{
		{"reads the label that closed the previous turn",
			[]router.ConversationMessage{user("write it"), assistant(full), user("go on")}, want, true},
		{"keeps the previous turn's label through the current turn's tool loop",
			[]router.ConversationMessage{user("write it"), assistant(full), user("go on"), toolCall(), toolResult(), assistant("checking"), toolCall(), toolResult()}, want, true},
		{"resolves a repeated label to the turn before",
			[]router.ConversationMessage{user("write it"), assistant(full), user("go on"), toolCall(), toolResult(), assistant("Still running.\n@cycle ="), user("and now?")}, want, true},
		{"ignores an older label when the previous turn ended without one",
			[]router.ConversationMessage{user("write it"), assistant(full), user("go on"), assistant("Done."), user("and now?")}, taskarm.Label{}, false},
		{"rejects a label with the wrong number of words",
			[]router.ConversationMessage{user("write it"), assistant("@cycle export verify running | Short."), user("go on")}, taskarm.Label{}, false},
		{"rejects a label that is not the last line",
			[]router.ConversationMessage{user("write it"), assistant(full + "\nOne more thing."), user("go on")}, taskarm.Label{}, false},
		{"finds nothing on the first turn",
			[]router.ConversationMessage{user("write it")}, taskarm.Label{}, false},
		{"finds nothing when a repeated label has no antecedent",
			[]router.ConversationMessage{user("write it"), assistant("@cycle ="), user("go on")}, taskarm.Label{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := taskarm.PreviousTurnLabel(tc.messages)
			assert.Equal(t, tc.found, found)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAssignIsStablePerTaskAndSplitsByTreatmentPct(t *testing.T) {
	steps := []string{"verify", "review"}
	session := []byte("session-a")
	label := taskarm.Label{Goal: "export", Task: "csv-writer", Step: "verify"}

	first, ok := taskarm.Assign(session, label, steps, 50)
	require.True(t, ok)
	laterStep := label
	laterStep.Step = "review"
	second, ok := taskarm.Assign(session, laterStep, steps, 50)
	require.True(t, ok)
	assert.Equal(t, first.Arm, second.Arm, "a task keeps its arm when it moves between eligible steps")
	assert.Equal(t, first.TaskKey, second.TaskKey)
	assert.Equal(t, "review", second.Step)
	assert.NotContains(t, first.TaskKey, "csv-writer")

	treated := 0
	const tasks = 4000
	for i := range tasks {
		assignment, ok := taskarm.Assign(session, taskarm.Label{Goal: "g", Task: fmt.Sprintf("task-%d", i), Step: "verify"}, steps, 25)
		require.True(t, ok)
		if assignment.Arm == taskarm.ArmTreatment {
			treated++
			assert.InDelta(t, 0.25, assignment.Propensity, 1e-9)
		} else {
			assert.InDelta(t, 0.75, assignment.Propensity, 1e-9)
		}
	}
	assert.InDelta(t, 0.25, float64(treated)/tasks, 0.03)
}

func TestAssignEligibility(t *testing.T) {
	label := taskarm.Label{Goal: "export", Task: "csv-writer", Step: "implement"}
	cases := []struct {
		name  string
		steps []string
		pct   int
		step  string
		ok    bool
		arm   taskarm.Arm
	}{
		{"skips a step outside the list", []string{"verify"}, 50, "implement", false, ""},
		{"skips when no share is treated", []string{"verify"}, 0, "verify", false, ""},
		{"skips an out-of-range share", []string{"verify"}, 101, "verify", false, ""},
		{"treats every eligible task at 100", []string{"verify"}, 100, "verify", true, taskarm.ArmTreatment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label.Step = tc.step
			got, ok := taskarm.Assign([]byte("s"), label, tc.steps, tc.pct)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.arm, got.Arm)
		})
	}
}

func TestSteps(t *testing.T) {
	assert.Equal(t, []string{"verify", "review"}, taskarm.Steps(" verify, review ,"))
	assert.Empty(t, taskarm.Steps(""))
}
