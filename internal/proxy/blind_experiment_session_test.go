package proxy_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionModeExperimentRepo struct{ routerOnPercentage int }

func (repo sessionModeExperimentRepo) GetForUser(context.Context, string, string) (auth.BlindExperimentRecord, error) {
	return auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: repo.routerOnPercentage, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	}, nil
}

func sessionModeArm(t *testing.T, routerOnPercentage int, headers http.Header) auth.BlindExperimentState {
	t.Helper()
	svc := auth.NewService(nil, nil, nil, &captureUserRepo{}, nil, auth.NoOpUserCache{}, time.Now).WithBlindExperiments(sessionModeExperimentRepo{routerOnPercentage: routerOnPercentage}, nil)
	identity := proxy.ClientIdentityFromHeaders(headers)
	identity.Email = "alice@example.com"
	ctx := requestcontext.WithClientIdentity(context.Background(), identity)
	ctx = proxy.ResolveUserFromContext(ctx, svc, &auth.Installation{ID: "inst-1"}, &auth.APIKey{ID: "key-1"})
	state, active := auth.BlindExperimentFrom(ctx)
	require.True(t, active)
	return state
}

func TestBlindExperimentSessionModeSplitsSessionsAndKeepsSubagentsWithParent(t *testing.T) {
	arms := map[auth.BlindExperimentArm]bool{}
	for index := range 40 {
		sessionID := "session-" + strconv.Itoa(index)
		claudeMain := sessionModeArm(t, 50, http.Header{"X-Claude-Code-Session-Id": {sessionID}})
		claudeTask := sessionModeArm(t, 50, http.Header{"X-Claude-Code-Session-Id": {sessionID}, "X-Claude-Code-Agent-Id": {"agent-1"}})
		codexMain := sessionModeArm(t, 50, http.Header{"Session-Id": {sessionID}, "Thread-Id": {sessionID}})
		codexSubagent := sessionModeArm(t, 50, http.Header{
			"Session-Id": {sessionID}, "Thread-Id": {"child-thread-" + strconv.Itoa(index)},
			"X-Codex-Parent-Thread-Id": {sessionID}, "X-Openai-Subagent": {"collab_spawn"},
		})
		require.NotEmpty(t, claudeMain.SessionSubjectKey)
		assert.Equal(t, claudeMain.SessionSubjectKey, claudeTask.SessionSubjectKey)
		assert.Equal(t, claudeMain.Arm, claudeTask.Arm)
		assert.Equal(t, codexMain.SessionSubjectKey, codexSubagent.SessionSubjectKey)
		assert.Equal(t, codexMain.Arm, codexSubagent.Arm)
		arms[claudeMain.Arm] = true
	}
	assert.True(t, arms[auth.BlindExperimentArmRouterOn])
	assert.True(t, arms[auth.BlindExperimentArmPassthrough])
}

func TestBlindExperimentSessionModeWithoutSessionIDUsesUserDraw(t *testing.T) {
	state := sessionModeArm(t, 100, http.Header{})
	assert.Empty(t, state.SessionSubjectKey)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
}
