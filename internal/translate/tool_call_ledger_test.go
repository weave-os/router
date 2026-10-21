package translate_test

import (
	"testing"

	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolCallLedger_IdlessCallKeepsGeneratedIDWhenSourceArrivesLate(t *testing.T) {
	l := translate.NewToolCallLedger()
	entry := l.AppendArguments(4, "", "", `{"path":`)
	generated := entry.ExternalID
	require.NotEmpty(t, generated)
	assert.Empty(t, entry.SourceID)

	entry = l.AppendArguments(4, "call_real", "Read", `"a.go"}`)
	assert.Equal(t, generated, entry.ExternalID)
	assert.Equal(t, "call_real", entry.SourceID)
	assert.Equal(t, "Read", entry.Name)
	assert.Equal(t, `{"path":"a.go"}`, entry.Arguments.String())
	assert.True(t, entry.HasSourceID("call_real"))
}

func TestToolCallLedger_ArgumentSelectionAndRepair(t *testing.T) {
	for _, tc := range []struct {
		name, fragments, terminal, want string
		issue                           bool
	}{
		{"empty uses terminal", "", `{"path":"terminal.txt"}`, `{"path":"terminal.txt"}`, false},
		{"valid fragments win", `{"path":"stream.txt"}`, `{"path":"terminal.txt"}`, `{"path":"stream.txt"}`, false},
		{"invalid fragments use terminal", `{"path":`, `{"path":"terminal.txt"}`, `{"path":"terminal.txt"}`, false},
		{"invalid terminal cannot replace fragments", `{"path":"stream.txt"}`, "invalid", `{"path":"stream.txt"}`, false},
		{"malformed fragments repaired", `{"path":"stream.txt"`, "invalid", `{"path":"stream.txt"}`, true},
		{"unrepairable falls back to object", "invalid", "invalid", `{}`, true},
		{"empty zero parameter call", "", "", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := translate.NewToolCallLedger()
			ledger.AppendArguments(0, "", "Read", tc.fragments)
			call := ledger.Close(0, "late", "", tc.terminal).Accept(nil)
			assert.Equal(t, tc.want, call.Arguments)
			assert.Equal(t, tc.issue, call.Issue != nil)
			assert.Equal(t, translate.ToolCallNotSuppressed, call.Suppression)
		})
	}
}

func TestToolCallLedger_AcceptanceIsFinalAndRetainsAliases(t *testing.T) {
	ledger := translate.NewToolCallLedger()
	entry := ledger.AppendArguments(3, "", "", `{"path":`)
	id := entry.ExternalID
	entry = ledger.AppendArguments(3, "late", "Read", `"synthetic.txt"}`)
	call := entry.Accept(nil)
	assert.Equal(t, id, call.ID)
	assert.Equal(t, "Read", call.Name)
	assert.Equal(t, `{"path":"synthetic.txt"}`, call.Arguments)
	ledger.SetArguments(3, `{"path":"replacement.txt"}`)
	ledger.AppendArguments(3, "another", "Write", "invalid")
	assert.Equal(t, call, ledger.Close(3, "final", "Write", `{}`).Accept(nil))
	assert.True(t, entry.HasSourceID("late"))
	assert.True(t, entry.HasSourceID("another"))
	assert.True(t, entry.HasSourceID("final"))

	nameless := ledger.Close(7, "", "", `{}`).Accept(nil)
	assert.Equal(t, translate.ToolCallMissingName, nameless.Suppression)
	assert.Equal(t, 1, ledger.SuppressedCount())
	assert.Equal(t, nameless, ledger.Close(7, "late", "Read", `{}`).Accept(nil))
}

func TestToolCallLedger_CompleteArgumentsOverrideFragments(t *testing.T) {
	ledger := translate.NewToolCallLedger()
	ledger.AppendArguments(0, "call_synthetic", "Read", `{"path":"fragment.txt"}`)
	ledger.SetArguments(0, `{"path":"done.txt"}`)
	call := ledger.Close(0, "", "", `{"path":"terminal.txt"}`).Accept(nil)
	assert.Equal(t, `{"path":"done.txt"}`, call.Arguments)

	ledger.AppendArguments(1, "", "Read", `{"path":"discarded.txt"}`)
	ledger.SetArguments(1, "")
	call = ledger.Close(1, "", "", `{"path":"terminal.txt"}`).Accept(nil)
	assert.Equal(t, `{"path":"terminal.txt"}`, call.Arguments)
}

func TestToolCallLedger_SchemaRepairsRemainFailOpen(t *testing.T) {
	validator := toolcheck.Compile([]byte(`[{"name":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"},"pages":{"type":"string"}},"required":["path"]}}]`))
	ledger := translate.NewToolCallLedger()
	call := ledger.Close(0, "", "Read", `{"path":"synthetic.txt","pages":null}`).Accept(validator)
	assert.JSONEq(t, `{"path":"synthetic.txt"}`, call.Arguments)
	assert.Equal(t, translate.ToolCallNotSuppressed, call.Suppression)

	call = ledger.Close(1, "", "Read", `{"pages":"1"}`).Accept(validator)
	assert.Equal(t, translate.ToolCallNotSuppressed, call.Suppression)
	assert.JSONEq(t, `{"pages":"1"}`, call.Arguments)
	require.NotNil(t, call.Issue)
	assert.Equal(t, toolcheck.BucketSchemaMismatch, call.Issue.Bucket)
	assert.False(t, call.Issue.Repaired)

	call = ledger.Close(2, "", "Unknown", `{}`).Accept(validator)
	assert.Equal(t, translate.ToolCallNotSuppressed, call.Suppression)
	require.NotNil(t, call.Issue)
	assert.Equal(t, toolcheck.BucketUnknownTool, call.Issue.Bucket)
}

func TestToolCallLedger_ParallelAndDuplicateSourceIDsNeverCollide(t *testing.T) {
	l := translate.NewToolCallLedger()
	first := l.Upsert(0, "duplicate", "Read")
	second := l.Upsert(1, "duplicate", "Write")
	assert.NotEqual(t, first.ExternalID, second.ExternalID,
		"a reused source ID must remain scoped to its source index")

	closed := l.Close(1, "duplicate", "", `{"path":"b.go"}`)
	assert.True(t, closed.Closed)
	assert.False(t, closed.Open)
	assert.Equal(t, `{"path":"b.go"}`, closed.Arguments.String())
}
