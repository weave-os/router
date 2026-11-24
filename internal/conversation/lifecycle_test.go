package conversation_test

import (
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestLifecycleOwnsCompactionTracker(t *testing.T) {
	lifecycle := conversation.NewLifecycle()
	key := [sessionpin.SessionKeyLen]byte{1}
	installation := uuid.New()

	assert.NotNil(t, lifecycle.Compaction())
	lifecycle.Compaction().CheckAndRecord(key, installation, "main", 9, 0)
	assert.True(t, lifecycle.Compaction().Tracks(key, installation, "main"))
}

func TestZeroLifecycleHasNoTrackers(t *testing.T) {
	var nilLifecycle *conversation.Lifecycle
	assert.Nil(t, nilLifecycle.Compaction())
	assert.Nil(t, nilLifecycle.NoProgress())
	assert.False(t, nilLifecycle.TextRepetitionBreak())
	assert.Nil(t, (&conversation.Lifecycle{}).Compaction())
}

type configSummarizer struct{ handover.Summarizer }

type configFeedbackStore struct{ feedback.RouterFeedbackStore }

type configStrategies struct{ sessionstrategy.Store }

func TestConfigureSetsConversationDependencies(t *testing.T) {
	summarizer := &configSummarizer{}
	feedbackStore := &configFeedbackStore{}
	strategies := &configStrategies{}
	links := feedback.NewLinks(nil, nil, "https://feedback.example", nil, nil)
	secret := strings.Repeat("s", 32)
	off := false

	lifecycle := conversation.NewLifecycle()
	lifecycle.Configure(conversation.Config{
		Summarizer:          summarizer,
		RouterFeedbackStore: feedbackStore,
		SessionStrategies:   strategies,
		FeedbackLinks:       links,
		PiHandoffSecret:     secret,
		TextRepetitionBreak: &off,
	})

	assert.Same(t, summarizer, lifecycle.Summarizer())
	assert.Same(t, feedbackStore, lifecycle.RouterFeedbackStore())
	assert.Same(t, strategies, lifecycle.SessionStrategies())
	assert.Same(t, links, lifecycle.FeedbackLinks())
	assert.Equal(t, []byte(secret), lifecycle.PiHandoff(time.Now).Secret)
	assert.False(t, lifecycle.TextRepetitionBreak())
}

func TestConfigureKeepsTextRepetitionBreakDefaultWhenUnset(t *testing.T) {
	lifecycle := conversation.NewLifecycle()
	lifecycle.Configure(conversation.Config{})
	assert.True(t, lifecycle.TextRepetitionBreak())
}

func TestConfigureIgnoresPiHandoffSecretUnder32Bytes(t *testing.T) {
	lifecycle := conversation.NewLifecycle()
	lifecycle.Configure(conversation.Config{PiHandoffSecret: strings.Repeat("s", 31)})
	assert.Empty(t, lifecycle.PiHandoff(time.Now).Secret)
}
