package proxy

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func codexUsageLimitAttempt(owner FailureOwner) dispatch.TracedAttempt {
	at := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	quota := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":` +
			strconv.FormatInt(at.Add(89*time.Minute+30*time.Second).Unix(), 10) + `}}`)}
	return dispatch.TracedAttempt{
		Target:    inference.Target{Provider: providers.ProviderOpenAI},
		Err:       errors.Join(codexSubscriptionModelUnavailable("gpt-test"), quota),
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(TurnErrorModelNotFound), FailureOwner: string(owner)},
		At:        at,
	}
}

func TestCodexUsageLimitWordingSurvivesSyntheticModelUnavailable(t *testing.T) {
	t.Parallel()
	for _, owner := range []FailureOwner{FailureOwnerRouter, FailureOwnerProvider, FailureOwnerClient} {
		a := codexUsageLimitAttempt(owner)
		assert.Equal(t, "hit the Codex usage limit (resets in 1h 29m)", failureWordingFor(a).badge, owner)
		assert.Equal(t, translate.RescueDetailPrefix+"OpenAI hit the Codex usage limit (resets in 1h 29m)\n\n", rescueDetailLine(a), owner)
		assert.Equal(t, "OpenAI hit the Codex usage limit (resets in 1h 29m) after 1 router retry: The usage limit has been reached",
			TerminalErrorMessage(attributeAttempt(a, 1, a.Err)), owner)
	}
}

func TestCodexUsageLimitWordingWithoutReset(t *testing.T) {
	t.Parallel()
	a := dispatch.TracedAttempt{
		Target:    inference.Target{Provider: providers.ProviderOpenAI},
		Err:       &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"insufficient_quota"}}`)},
		Diagnosis: inference.AttemptDiagnosis{FailureOwner: string(FailureOwnerProvider)},
	}
	assert.Equal(t, "hit the Codex usage limit", failureWordingFor(a).badge)
	assert.Equal(t, "OpenAI hit the Codex usage limit.", terminalMessageFor(a))
}

func TestShortDuration(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		30 * time.Second:                "under 1m",
		5*time.Minute + 59*time.Second:  "5m",
		2 * time.Hour:                   "2h",
		89*time.Minute + 30*time.Second: "1h 29m",
	} {
		assert.Equal(t, want, shortDuration(d), d)
	}
}
