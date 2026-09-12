package proxy_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ccTaskToolTurnBody is a Claude Code turn carrying the task-list tools plus
// one of their system reminders, as it arrives on /v1/messages.
const ccTaskToolTurnBody = `{
	"model":"claude-opus-4-7",
	"system":"You are Claude Code.",
	"messages":[{"role":"user","content":[
		{"type":"text","text":"fix the bug"},
		{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
	]}],
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

func ccTaskToolProvider() *fakeProvider {
	return &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}}
}

// The org override is the lever the A/B runs on, so it must reach the emitted
// cross-vendor body — and the mitigation counters must follow it.
func TestProxyMessages_CCTaskToolsCrossVendor_OrgOverrideReachesUpstream(t *testing.T) {
	for _, tc := range []struct {
		name              string
		override          *bool
		wantTaskTools     bool
		wantToolsStripped string
		wantRemStripped   string
	}{
		{
			name:              "default off strips the task tools and their reminder",
			wantToolsStripped: "cc_only_tools_stripped=4",
			wantRemStripped:   "cc_task_reminders_stripped=1",
		},
		{
			name:              "org override on keeps the task tools and their reminder",
			override:          boolPtr(true),
			wantTaskTools:     true,
			wantToolsStripped: "cc_only_tools_stripped=0",
			wantRemStripped:   "cc_task_reminders_stripped=0",
		},
		{
			name:              "org override off pins today's behaviour",
			override:          boolPtr(false),
			wantToolsStripped: "cc_only_tools_stripped=4",
			wantRemStripped:   "cc_task_reminders_stripped=1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observability.Get()
			var logBuf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			provider := ccTaskToolProvider()
			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "test"}},
				map[string]providers.Client{providers.ProviderOpenAI: provider},
				nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
			)

			ctx := context.Background()
			if tc.override != nil {
				ctx = flags.WithOverrides(ctx, flags.Overrides{
					Bools: map[flags.Key]bool{flags.KeyCCTaskToolsCrossVendor: *tc.override},
				})
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(ccTaskToolTurnBody))
			require.NoError(t, svc.ProxyMessages(ctx, []byte(ccTaskToolTurnBody), rec, req))

			require.Len(t, provider.proxyBodies, 1)
			sent := string(provider.proxyBodies[0])
			assert.Contains(t, sent, `"Read"`, "ordinary tools always survive")
			assert.Contains(t, sent, `"Task"`, "orchestration tools survive on the default rollout")
			for _, name := range []string{"TaskCreate", "TaskUpdate", "TaskGet", "TaskList"} {
				if tc.wantTaskTools {
					assert.Contains(t, sent, `"`+name+`"`)
				} else {
					assert.NotContains(t, sent, `"`+name+`"`)
				}
			}
			assert.Equal(t, tc.wantTaskTools, strings.Contains(sent, "task tools haven"),
				"the reminder survives exactly when the tools it names do")

			logs := logBuf.String()
			assert.Contains(t, logs, tc.wantToolsStripped)
			assert.Contains(t, logs, tc.wantRemStripped)
		})
	}
}
