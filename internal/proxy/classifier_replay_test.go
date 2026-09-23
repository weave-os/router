package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestClassifierClaudeEnvironmentRefresh(t *testing.T) {
	gitSnapshot := "\n\ngitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\n\nCurrent branch: HEAD\n\nMain branch (you will usually use this for PRs): main\n\nStatus:\n(clean)\n\nRecent commits:\n"
	dateContext := classifierClaudeContextPrefix + "# claudeMd\nSynthetic repository instructions.\n# currentDate\nToday's date is 2026-01-01.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n\n"
	var requests []router.AtomicClassificationRequest
	svc, ctx, _ := classifierSessionFixture(t, func(ctx context.Context, request router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		requests = append(requests, request)
		return classifierMedium(ctx, request)
	})
	ctx = classifierAdmit(t, svc, ctx)
	observation := classifierTestObservation(
		classifierTestText(translate.EscalationRoleSystem, "Persistent instructions."+gitSnapshot),
		translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{
			{Type: translate.EscalationBlockText, Text: dateContext},
			{Type: translate.EscalationBlockText, Text: "First request."},
		}},
	)
	first, err := classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, first)
	require.NoError(t, err)
	require.Equal(t, dateContext+"\n\nFirst request.", requests[0].User.CurrentUserMessage)
	require.Equal(t, "Persistent instructions."+gitSnapshot, observation.Messages[0].Blocks[0].Text)

	observation.Messages[0].Blocks[0].Text += "abcdef0 synthetic checkpoint"
	observation.Messages[1].Blocks[0].Text = strings.Replace(dateContext, "2026-01-01", "2026-01-02", 1)
	observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleAssistant, "First answer."), classifierTestText(translate.EscalationRoleUser, "Follow-up."))
	next, err := classifierContextForCall(observation)
	require.NoError(t, err)
	require.Equal(t, first.RootTurnDigest, next.RootTurnDigest)
	_, err = svc.classifyThread(ctx, next)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.Equal(t, "Follow-up.", requests[1].User.CurrentUserMessage)
	require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "First answer.", Complexity: router.ClassifierMedium}}, requests[1].User.PrecedingResponses)

	for _, index := range []int{0, 1} {
		original := observation.Messages[index].Blocks[0].Text
		observation.Messages[index].Blocks[0].Text = "Rewritten instructions.\n" + original
		changed, err := classifierContextForCall(observation)
		require.NoError(t, err)
		_, err = svc.classifyThread(ctx, changed)
		require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
		observation.Messages[index].Blocks[0].Text = original
	}
	require.Len(t, requests, 2, "instruction changes still fail before inference")
}

func TestClassifierReplayMetadataScope(t *testing.T) {
	dateContext := classifierClaudeContextPrefix + "# currentDate\nToday's date is 2026-01-01.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n\n"
	message := classifierTestText(translate.EscalationRoleUser, dateContext)
	require.NotEqual(t, message, classifierReplayMessage(message, 1))
	require.Equal(t, message, classifierReplayMessage(message, 3), "later human messages remain identity-bearing")
	message.Blocks[0].Text += "Additional instructions."
	require.Equal(t, message, classifierReplayMessage(message, 1), "unrecognized suffixes remain identity-bearing")
	message.Blocks[0].Text = strings.TrimPrefix(dateContext, classifierClaudeContextPrefix)
	require.Equal(t, message, classifierReplayMessage(message, 1), "a date in ordinary user text is not metadata")
}
