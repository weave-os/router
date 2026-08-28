package selection_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/hmm/selection"
	"workweave/router/internal/router/policy"
)

type recordingHandler struct {
	records *[]slog.Record
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

func TestShadowAgreeIsEffortInsensitive(t *testing.T) {
	var records []slog.Record
	ctx := observability.WithLogger(context.Background(), slog.New(recordingHandler{records: &records}))

	shadow := selection.Shadow(testRoster())
	shadow(ctx, policy.SelectionObservation{
		Harness:      "codex",
		SidecarGroup: "effort",
		// The served pick is the base model ID; the roster arm carries :high.
		SidecarPick: "vendor-a/deep",
		RankedFallback: []policy.PreviewGroup{
			{Group: "effort"},
		},
		CandidateRosterIDs: []string{"vendor-a/deep"},
	})

	require.Len(t, records, 1)
	var agree, found bool
	var shadowArm string
	records[0].Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "agree":
			agree = a.Value.Bool()
			found = true
		case "shadow_arm":
			shadowArm = a.Value.String()
		}
		return true
	})
	require.True(t, found)
	assert.True(t, agree)
	assert.Equal(t, "vendor-a/deep:high", shadowArm)
}
