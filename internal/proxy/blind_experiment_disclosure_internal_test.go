package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blindExperimentCtx(arm auth.BlindExperimentArm) context.Context {
	return context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Active: true, Arm: arm})
}

func TestRoutingMarkerHiddenForBlindExperimentSubjects(t *testing.T) {
	const marker = "✦ **Weave Router** → deepseek/deepseek-v4-flash · best pick for this turn\n\n"
	for _, arm := range []auth.BlindExperimentArm{auth.BlindExperimentArmRouterOn, auth.BlindExperimentArmPassthrough} {
		assert.Empty(t, suppressMarkerIfRequested(blindExperimentCtx(arm), http.Header{}, marker), "arm %s", arm)
	}

	inactive := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Arm: auth.BlindExperimentArmRouterOn})
	assert.Equal(t, marker, suppressMarkerIfRequested(inactive, http.Header{}, marker))
}

func TestLoopBreaksDoNotNameModelForBlindExperimentSubjects(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"retry"}]}`,
	} {
		env, err := translate.ParseAnthropic([]byte(body))
		require.NoError(t, err)
		for name, run := range map[string]func(context.Context, http.ResponseWriter) error{
			"no_progress": func(ctx context.Context, w http.ResponseWriter) error {
				return (&Service{}).handleNoProgressBreak(ctx, w, env, 3, uuid.New(), [sessionpin.SessionKeyLen]byte{1}, "maximum", "synthetic-served-model", providers.ProviderOpenAI, 1)
			},
			"text_repetition": func(ctx context.Context, w http.ResponseWriter) error {
				return (&Service{}).handleTextRepetitionBreak(ctx, w, env, 3, "synthetic", uuid.New(), [sessionpin.SessionKeyLen]byte{1}, "maximum", "synthetic-served-model", providers.ProviderOpenAI, 1)
			},
		} {
			rec := httptest.NewRecorder()
			require.NoError(t, run(blindExperimentCtx(auth.BlindExperimentArmRouterOn), rec), name)
			out := rec.Body.String()
			assert.Contains(t, out, "Stopped this turn", name)
			assert.NotContains(t, out, "synthetic-served-model", name)
			assert.NotContains(t, out, "Weave Router", name)
			assert.NotContains(t, out, providers.ProviderOpenAI, name)

			rec = httptest.NewRecorder()
			require.NoError(t, run(context.Background(), rec), name)
			assert.Contains(t, rec.Body.String(), "synthetic-served-model", name)
		}
	}
}
