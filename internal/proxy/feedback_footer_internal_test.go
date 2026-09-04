package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/router/turntype"

	"github.com/stretchr/testify/assert"
)

type footerFakeStore struct{}

func (footerFakeStore) InsertRouterFeedback(context.Context, RouterFeedbackEvent) error { return nil }

func TestFeedbackFooter_ClientGating(t *testing.T) {
	withStore := (&Service{}).WithRouterFeedbackStore(footerFakeStore{})

	t.Run("terminal agents get the link-free rating hint", func(t *testing.T) {
		for _, app := range []string{ClientAppClaudeCode, ClientAppOpencode} {
			footer := withStore.feedbackFooter(context.Background(), app, turntype.MainLoop, false)
			assert.Equal(t, feedbackFooterText, footer, "expected hint for %q", app)
			assert.NotContains(t, footer, "http", "footer must never embed a raw link")
		}
		codex := withStore.feedbackFooter(context.Background(), ClientAppCodex, turntype.MainLoop, false)
		assert.Equal(t, feedbackFooterTextCodex, codex)
		assert.Contains(t, codex, "$rf +")
		assert.NotContains(t, codex, "`/rf")
		assert.NotContains(t, codex, "http")
	})

	t.Run("ide and unknown clients are suppressed", func(t *testing.T) {
		for _, app := range []string{ClientAppCursor, ClientAppGeminiCLI, "", "some-bot"} {
			assert.Empty(t, withStore.feedbackFooter(context.Background(), app, turntype.MainLoop, false), "expected no footer for %q", app)
		}
	})

	t.Run("no durable store suppresses the hint entirely", func(t *testing.T) {
		assert.Empty(t, (&Service{}).feedbackFooter(context.Background(), ClientAppClaudeCode, turntype.MainLoop, false), "advertising a command we cannot record is misleading")
	})

	t.Run("hidden terminal surfaces suppress the hint even for a main-loop turn", func(t *testing.T) {
		hiddenCtx := context.WithValue(context.Background(), InstallationHideTerminalSurfacesContextKey{}, true)
		assert.Empty(t, withStore.feedbackFooter(hiddenCtx, ClientAppClaudeCode, turntype.MainLoop, false), "hidden orgs get no /rf hint")
	})

	t.Run("an echoed footer with no new human turn suppresses the hint", func(t *testing.T) {
		for _, tt := range []turntype.TurnType{turntype.MainLoop, turntype.ToolResult} {
			assert.Empty(t, withStore.feedbackFooter(context.Background(), ClientAppClaudeCode, tt, true), "expected no footer for %q — continuation chains would stack duplicate hints", tt)
		}
	})
}

func TestFeedbackFooter_TurnTypeGating(t *testing.T) {
	withStore := (&Service{}).WithRouterFeedbackStore(footerFakeStore{})

	t.Run("the user's own conversation turns get the hint", func(t *testing.T) {
		for _, tt := range []turntype.TurnType{turntype.MainLoop, turntype.ToolResult} {
			assert.Equal(t, feedbackFooterText, withStore.feedbackFooter(context.Background(), ClientAppClaudeCode, tt, false), "expected hint for %q", tt)
		}
	})

	t.Run("subagent and machine turns are suppressed", func(t *testing.T) {
		for _, tt := range []turntype.TurnType{
			turntype.SubAgentDispatch,
			turntype.Compaction,
			turntype.Probe,
			turntype.TitleGen,
			turntype.Classifier,
		} {
			assert.Empty(t, withStore.feedbackFooter(context.Background(), ClientAppClaudeCode, tt, false), "expected no footer for %q — hint would strand under output the user never initiated", tt)
		}
	})
}
