package main

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/providers/openaicompat"

	"github.com/stretchr/testify/assert"
)

var upstreamHTTPEnvNames = []string{
	"ROUTER_SSE_IDLE_TIMEOUT_SECONDS", "ROUTER_RESPONSES_SSE_IDLE_TIMEOUT_SECONDS",
	"ROUTER_RESPONSES_OUTPUT_STALL_TIMEOUT_SECONDS", "ROUTER_OUTPUT_STALL_TIMEOUT_SECONDS",
	"ROUTER_THROUGHPUT_WINDOW_SECONDS", "ROUTER_THROUGHPUT_MIN_ELAPSED_SECONDS",
	"ROUTER_MIN_THROUGHPUT_DELTAS_PER_WINDOW", "ROUTER_H2_READ_IDLE_TIMEOUT_SECONDS",
	"ROUTER_H2_PING_TIMEOUT_SECONDS", "ROUTER_RESTRICT_UPSTREAM_EGRESS", "ROUTER_DEPLOYMENT_MODE",
	"ROUTER_ANTHROPIC_LARGE_PROMPT_HEADER_TIMEOUT_SECONDS", "ROUTER_GROK_HEADER_TIMEOUT_SECONDS",
}

func clearUpstreamHTTPEnv(t *testing.T) {
	t.Helper()
	for _, name := range upstreamHTTPEnvNames {
		t.Setenv(name, "")
	}
}

func withoutProxy(t *testing.T, cfg httputil.Config) httputil.Config {
	t.Helper()
	assert.Equal(t, reflect.ValueOf(http.ProxyFromEnvironment).Pointer(), reflect.ValueOf(cfg.Proxy).Pointer(), "upstream transports honor HTTP(S)_PROXY/NO_PROXY")
	cfg.Proxy = nil
	return cfg
}

func TestUpstreamHTTPConfigFromEnvDefaults(t *testing.T) {
	clearUpstreamHTTPEnv(t)
	assert.Equal(t, httputil.DefaultConfig(), withoutProxy(t, upstreamHTTPConfigFromEnv()))
	assert.Equal(t, anthropic.Config{LargePromptResponseHeaderTimeout: 120 * time.Second}, anthropicConfigFromEnv())
	assert.Equal(t, openaicompat.Config{GrokResponseHeaderTimeout: 90 * time.Second}, openAICompatConfigFromEnv())
}

func TestUpstreamHTTPConfigFromEnvOverrides(t *testing.T) {
	clearUpstreamHTTPEnv(t)
	for name, value := range map[string]string{
		"ROUTER_SSE_IDLE_TIMEOUT_SECONDS":                      "11",
		"ROUTER_RESPONSES_SSE_IDLE_TIMEOUT_SECONDS":            "12",
		"ROUTER_RESPONSES_OUTPUT_STALL_TIMEOUT_SECONDS":        "13",
		"ROUTER_OUTPUT_STALL_TIMEOUT_SECONDS":                  "14",
		"ROUTER_THROUGHPUT_WINDOW_SECONDS":                     "15",
		"ROUTER_THROUGHPUT_MIN_ELAPSED_SECONDS":                "16",
		"ROUTER_MIN_THROUGHPUT_DELTAS_PER_WINDOW":              "0",
		"ROUTER_H2_READ_IDLE_TIMEOUT_SECONDS":                  "18",
		"ROUTER_H2_PING_TIMEOUT_SECONDS":                       "19",
		"ROUTER_DEPLOYMENT_MODE":                               "managed",
		"ROUTER_ANTHROPIC_LARGE_PROMPT_HEADER_TIMEOUT_SECONDS": "200",
		"ROUTER_GROK_HEADER_TIMEOUT_SECONDS":                   "60",
	} {
		t.Setenv(name, value)
	}
	assert.Equal(t, httputil.Config{
		SSEIdleTimeout:               11 * time.Second,
		ResponsesSSEIdleTimeout:      12 * time.Second,
		ResponsesOutputStallTimeout:  13 * time.Second,
		OutputStallTimeout:           14 * time.Second,
		ThroughputWindow:             15 * time.Second,
		ThroughputMinElapsed:         16 * time.Second,
		MinThroughputDeltasPerWindow: 0,
		H2ReadIdleTimeout:            18 * time.Second,
		H2PingTimeout:                19 * time.Second,
		PublicDestinationsOnly:       true,
	}, withoutProxy(t, upstreamHTTPConfigFromEnv()))
	assert.Equal(t, anthropic.Config{LargePromptResponseHeaderTimeout: 200 * time.Second}, anthropicConfigFromEnv())
	assert.Equal(t, openaicompat.Config{GrokResponseHeaderTimeout: 60 * time.Second}, openAICompatConfigFromEnv())
}

func TestUpstreamHTTPConfigFromEnvInvalidValuesFallBack(t *testing.T) {
	clearUpstreamHTTPEnv(t)
	t.Setenv("ROUTER_SSE_IDLE_TIMEOUT_SECONDS", "0")
	t.Setenv("ROUTER_MIN_THROUGHPUT_DELTAS_PER_WINDOW", "eight")
	t.Setenv("ROUTER_RESTRICT_UPSTREAM_EGRESS", "false")
	t.Setenv("ROUTER_DEPLOYMENT_MODE", "managed")
	t.Setenv("ROUTER_GROK_HEADER_TIMEOUT_SECONDS", "-1")

	got := upstreamHTTPConfigFromEnv()
	assert.Equal(t, 45*time.Second, got.SSEIdleTimeout)
	assert.Equal(t, 8, got.MinThroughputDeltasPerWindow)
	assert.False(t, got.PublicDestinationsOnly)
	assert.Equal(t, 90*time.Second, openAICompatConfigFromEnv().GrokResponseHeaderTimeout)
}
