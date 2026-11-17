package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseEnvFloatAcceptsZeroAndNegative(t *testing.T) {
	const key = "ROUTER_TEST_FLOAT"
	t.Setenv(key, "-0.5")
	assert.InDelta(t, -0.5, parseEnvFloat(key, 1), 0)
	t.Setenv(key, "0")
	assert.InDelta(t, 0.0, parseEnvFloat(key, 1), 0)
	t.Setenv(key, "x")
	assert.InDelta(t, 1.0, parseEnvFloat(key, 1), 0)
}

func TestParseEnvDurationMsRejectsZero(t *testing.T) {
	const key = "ROUTER_TEST_DURATION_MS"
	t.Setenv(key, "0")
	assert.Equal(t, time.Second, parseEnvDurationMs(key, time.Second))
	t.Setenv(key, "250")
	assert.Equal(t, 250*time.Millisecond, parseEnvDurationMs(key, time.Second))
}

func TestFeedbackLinkTTLZeroMeansNeverExpire(t *testing.T) {
	t.Setenv("ROUTER_FEEDBACK_LINK_TTL_SEC", "")
	assert.Equal(t, 30*24*time.Hour, feedbackLinkTTL())
	t.Setenv("ROUTER_FEEDBACK_LINK_TTL_SEC", "0")
	assert.Equal(t, time.Duration(0), feedbackLinkTTL())
	t.Setenv("ROUTER_FEEDBACK_LINK_TTL_SEC", "-1")
	assert.Equal(t, 30*24*time.Hour, feedbackLinkTTL())
}

func TestSSEKeepaliveIntervalZeroDisables(t *testing.T) {
	t.Setenv("ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS", "")
	assert.Equal(t, 15*time.Second, sseKeepaliveInterval())
	t.Setenv("ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS", "0")
	assert.Equal(t, time.Duration(0), sseKeepaliveInterval())
	t.Setenv("ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS", "-3")
	assert.Equal(t, 15*time.Second, sseKeepaliveInterval())
}

func TestRLSidecarHeadersNeedBothKeyAndSecret(t *testing.T) {
	t.Setenv("ROUTER_RL_SIDECAR_MODAL_KEY", "k")
	t.Setenv("ROUTER_RL_SIDECAR_MODAL_SECRET", "")
	assert.Nil(t, rlSidecarHeadersFromEnv())
	t.Setenv("ROUTER_RL_SIDECAR_MODAL_SECRET", "s")
	assert.Equal(t, map[string]string{
		"X-Weave-Band-Key":    "k",
		"X-Weave-Band-Secret": "s",
		"Authorization":       "Bearer k:s",
		"Modal-Key":           "k",
		"Modal-Secret":        "s",
	}, rlSidecarHeadersFromEnv())
}
