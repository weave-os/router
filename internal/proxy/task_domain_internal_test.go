package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/taskdomain"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func TestTaskInputIgnoresInjectedBlocksWithoutChangingRoot(t *testing.T) {
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "synthetic-session"})
	plain, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"Review parser\n<requirements>Preserve XML</requirements>"}]}`))
	require.NoError(t, err)
	plainInput := taskDomainInput(ctx, plain, "key-a", turntype.MainLoop)
	require.NotNil(t, plainInput)
	for _, injected := range []string{
		"<system-reminder>workspace setup</system-reminder>",
		"<system-reminder>updated workspace setup</system-reminder>",
		"<command-name>/status</command-name>",
		"<local-command-stdout>Status ready</local-command-stdout>",
	} {
		for _, position := range []int{0, 1, 2} {
			blocks := []map[string]string{
				{"type": "text", "text": "Review parser"},
				{"type": "text", "text": "<requirements>Preserve XML</requirements>"},
			}
			blocks = append(blocks[:position], append([]map[string]string{{"type": "text", "text": injected}}, blocks[position:]...)...)
			body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": blocks}}})
			require.NoError(t, err)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			input := taskDomainInput(ctx, env, "key-a", turntype.MainLoop)
			require.NotNil(t, input)
			assert.Equal(t, "Review parser\n<requirements>Preserve XML</requirements>", input.UserText)
			assert.Equal(t, plainInput.RootSHA256, input.RootSHA256)
			assert.False(t, input.Resume)
		}
	}
}

func TestTaskInputLogicalRootAndIsolation(t *testing.T) {
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "synthetic-session"})
	env, err := translate.ParseOpenAI([]byte(`{"messages":[
 {"role":"system","content":"system setup"},
 {"role":"user","content":"<system_instruction>workspace setup</system_instruction>"},
 {"role":"user","content":"/fm astra"},
 {"role":"assistant","content":"Model selected"},
 {"role":"user","content":"<environment_context>cwd=/workspace</environment_context>Review the deployment"},
 {"role":"user","content":"Focus on rollback safety"},
 {"role":"assistant","content":"I will inspect it"},
 {"role":"user","content":"Now check the tests"}]}`))
	require.NoError(t, err)
	input := taskDomainInput(ctx, env, "key-a", turntype.MainLoop)
	require.NotNil(t, input)
	assert.Equal(t, "Review the deployment\n\nFocus on rollback safety", input.UserText)
	assert.False(t, input.Resume)
	assert.True(t, taskdomain.ValidDigest(input.RootSHA256))
	otherKey := taskDomainInput(ctx, env, "key-b", turntype.MainLoop)
	assert.NotEqual(t, input.ConversationKey, otherKey.ConversationKey)
	otherSession := context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "other-session"})
	assert.NotEqual(t, input.ConversationKey, taskDomainInput(otherSession, env, "key-a", turntype.MainLoop).ConversationKey)
	version1 := requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "release-one"})
	version2 := requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "release-two"})
	assert.NotEqual(t, taskDomainInput(version1, env, "key-a", turntype.MainLoop).ConversationKey, taskDomainInput(version2, env, "key-a", turntype.MainLoop).ConversationKey)
	assert.Nil(t, taskDomainInput(context.Background(), env, "key-a", turntype.MainLoop))
	assert.Nil(t, taskDomainInput(ctx, env, "key-a", turntype.Compaction))
	child, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"Inspect the deployment tests"}]}`))
	require.NoError(t, err)
	childInput := taskDomainInput(ctx, child, "key-a", turntype.SubAgentDispatch)
	assert.Equal(t, input.ConversationKey, childInput.ConversationKey)
	assert.NotEqual(t, input.RootSHA256, childInput.RootSHA256)
}

func TestTaskInputAcrossFormatsAndResume(t *testing.T) {
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "synthetic-session"})
	for _, tc := range []struct {
		name, body string
		parse      func([]byte) (*translate.RequestEnvelope, error)
		want       string
		resume     bool
	}{
		{"anthropic", `{"messages":[{"role":"user","content":"Review parser"},{"role":"assistant","content":"Working"},{"role":"user","content":"follow up"}]}`, translate.ParseAnthropic, "Review parser", false},
		{"anthropic command prelude", `{"messages":[{"role":"user","content":[{"type":"text","text":"<command-message>status</command-message>\n<command-name>/status</command-name>"}]},{"role":"assistant","content":"Status ready"},{"role":"user","content":"Review parser"}]}`, translate.ParseAnthropic, "Review parser", false},
		{"anthropic injected task", `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>workspace setup</system-reminder>"},{"type":"text","text":"Review parser"}]}]}`, translate.ParseAnthropic, "Review parser", false},
		{"anthropic decorated command", `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>workspace setup</system-reminder>"},{"type":"text","text":"<command-name>/status</command-name>"}]},{"role":"assistant","content":"Status ready"},{"role":"user","content":"Review parser"}]}`, translate.ParseAnthropic, "Review parser", false},
		{"anthropic truncated", `{"messages":[{"role":"user","content":"<system-reminder>workspace setup</system-reminder>"},{"role":"assistant","content":"Earlier work"},{"role":"user","content":"continue"}]}`, translate.ParseAnthropic, "", true},
		{"anthropic later command", `{"messages":[{"role":"assistant","content":"Earlier work"},{"role":"user","content":"<command-name>/status</command-name>"},{"role":"assistant","content":"Status ready"},{"role":"user","content":"continue"}]}`, translate.ParseAnthropic, "", true},
		{"gemini", `{"contents":[{"role":"user","parts":[{"text":"Review parser"}]},{"role":"model","parts":[{"text":"Working"}]}]}`, translate.ParseGemini, "Review parser", false},
		{"path", `{"messages":[{"role":"user","content":"/src/parser.go needs tests"}]}`, translate.ParseOpenAI, "/src/parser.go needs tests", false},
		{"codex resume", `{"messages":[{"role":"user","content":"Another language model started to solve this problem and produced a summary of its thinking process. Summary: parser changes"}]}`, translate.ParseOpenAI, "", true},
		{"pi resume", `{"messages":[{"role":"user","content":"The conversation history before this point was compacted into the following summary: parser changes"}]}`, translate.ParseAnthropic, "", true},
		{"truncated", `{"messages":[{"role":"assistant","content":"Earlier work"},{"role":"user","content":"continue"}]}`, translate.ParseOpenAI, "", true},
		{"original before summary", `{"messages":[{"role":"user","content":"Review parser"},{"role":"user","content":"Another language model started to solve this problem and produced a summary"}]}`, translate.ParseOpenAI, "Review parser", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			input := taskDomainInput(ctx, env, "key-a", turntype.MainLoop)
			require.NotNil(t, input)
			assert.Equal(t, tc.want, input.UserText)
			assert.Equal(t, tc.resume, input.Resume)
		})
	}
}

type taskStoreFunc func(context.Context, taskdomain.Key, bool, func(context.Context) taskdomain.Outcome) (taskdomain.Outcome, error)

func (f taskStoreFunc) Resolve(ctx context.Context, key taskdomain.Key, resume bool, classify func(context.Context) taskdomain.Outcome) (taskdomain.Outcome, error) {
	return f(ctx, key, resume, classify)
}

type taskClassifierFunc func(context.Context, string) (taskdomain.Profile, error)

func (f taskClassifierFunc) Classify(ctx context.Context, text string) (taskdomain.Profile, error) {
	return f(ctx, text)
}

func TestTaskResolverCommitsOnlyTimelyValidPredictions(t *testing.T) {
	profile := taskdomain.Profile{taskdomain.UI: false, taskdomain.Logic: true, taskdomain.Data: false, taskdomain.Infra: false, taskdomain.Docs: false}
	input := taskdomain.Input{ConversationKey: strings.Repeat("a", 64), RootSHA256: strings.Repeat("b", 64), UserText: "Review parser"}
	for _, tc := range []struct {
		name     string
		classify taskClassifierFunc
		status   taskdomain.Status
	}{
		{"valid", func(context.Context, string) (taskdomain.Profile, error) { return profile, nil }, taskdomain.Ready},
		{"invalid", func(context.Context, string) (taskdomain.Profile, error) {
			return taskdomain.Profile{taskdomain.Logic: true}, nil
		}, taskdomain.Unavailable},
		{"failed", func(context.Context, string) (taskdomain.Profile, error) {
			return profile, errors.New("synthetic failure")
		}, taskdomain.Unavailable},
		{"late", func(ctx context.Context, _ string) (taskdomain.Profile, error) { <-ctx.Done(); return profile, nil }, taskdomain.TimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := TaskDomainResolver{ReleaseSHA256: strings.Repeat("c", 64), EvidenceSHA256: strings.Repeat("d", 64), Classifier: tc.classify,
				Store: taskStoreFunc(func(ctx context.Context, key taskdomain.Key, resume bool, classify func(context.Context) taskdomain.Outcome) (taskdomain.Outcome, error) {
					assert.Equal(t, input.RootSHA256, key.Root)
					assert.False(t, resume)
					return classify(ctx), nil
				})}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			outcome := resolver.Resolve(ctx, input)
			assert.Equal(t, tc.status, outcome.Status)
			if tc.status == taskdomain.Ready {
				assert.Equal(t, profile, outcome.Profile)
			} else {
				assert.Nil(t, outcome.Profile)
			}
		})
	}
}

func TestTaskResolverUnavailableEvidenceSkipsInference(t *testing.T) {
	resolver := TaskDomainResolver{ReleaseSHA256: strings.Repeat("c", 64)}
	assert.Equal(t, taskdomain.EvidenceUnavailable, resolver.Resolve(context.Background(), taskdomain.Input{}).Status)
}
