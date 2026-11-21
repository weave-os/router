package main

import (
	"net/http"
	"os"

	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/providers/openaicompat"
)

func upstreamHTTPConfigFromEnv() httputil.Config {
	defaults := httputil.DefaultConfig()
	return httputil.Config{
		SSEIdleTimeout:               httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_SSE_IDLE_TIMEOUT_SECONDS"), defaults.SSEIdleTimeout),
		ResponsesSSEIdleTimeout:      httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_RESPONSES_SSE_IDLE_TIMEOUT_SECONDS"), defaults.ResponsesSSEIdleTimeout),
		ResponsesOutputStallTimeout:  httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_RESPONSES_OUTPUT_STALL_TIMEOUT_SECONDS"), defaults.ResponsesOutputStallTimeout),
		OutputStallTimeout:           httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_OUTPUT_STALL_TIMEOUT_SECONDS"), defaults.OutputStallTimeout),
		ThroughputWindow:             httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_THROUGHPUT_WINDOW_SECONDS"), defaults.ThroughputWindow),
		ThroughputMinElapsed:         httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_THROUGHPUT_MIN_ELAPSED_SECONDS"), defaults.ThroughputMinElapsed),
		MinThroughputDeltasPerWindow: httputil.ParseInt(os.Getenv("ROUTER_MIN_THROUGHPUT_DELTAS_PER_WINDOW"), defaults.MinThroughputDeltasPerWindow),
		H2ReadIdleTimeout:            httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_H2_READ_IDLE_TIMEOUT_SECONDS"), defaults.H2ReadIdleTimeout),
		H2PingTimeout:                httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_H2_PING_TIMEOUT_SECONDS"), defaults.H2PingTimeout),
		PublicDestinationsOnly:       httputil.ParsePublicDestinationsOnly(os.Getenv("ROUTER_RESTRICT_UPSTREAM_EGRESS"), os.Getenv("ROUTER_DEPLOYMENT_MODE")),
		Proxy:                        http.ProxyFromEnvironment,
	}
}

func anthropicConfigFromEnv() anthropic.Config {
	return anthropic.Config{
		LargePromptResponseHeaderTimeout: httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_ANTHROPIC_LARGE_PROMPT_HEADER_TIMEOUT_SECONDS"), anthropic.DefaultLargePromptResponseHeaderTimeout),
	}
}

func openAICompatConfigFromEnv() openaicompat.Config {
	return openaicompat.Config{
		GrokResponseHeaderTimeout: httputil.ParseTimeoutSeconds(os.Getenv("ROUTER_GROK_HEADER_TIMEOUT_SECONDS"), openaicompat.DefaultGrokResponseHeaderTimeout),
	}
}

func init() {
	configureUpstreamHTTP()
}

func configureUpstreamHTTP() {
	httputil.Configure(upstreamHTTPConfigFromEnv())
	anthropic.Configure(anthropicConfigFromEnv())
	openaicompat.Configure(openAICompatConfigFromEnv())
}
