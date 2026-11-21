package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"weave-os/router/cmd/logconfig"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

const fixtureRoot = "internal/proxy/testdata/conformance"

type format int

const (
	formatOpenAIChat format = iota
	formatOpenAIResponses
	formatGemini
	formatAnthropic
)

type recordCase struct {
	fixture       string
	format        format
	model         string
	provider      string
	anthropicBody string
}

var cases = []recordCase{
	{"openai_chat/basic_text.upstream.sse", formatOpenAIChat, "deepseek/deepseek-v4-pro", providers.ProviderOpenRouter,
		`{"model":"deepseek/deepseek-v4-pro","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Say hi."}]}`},
	{"openai_chat/toolcall.upstream.sse", formatOpenAIChat, "deepseek/deepseek-v4-pro", providers.ProviderOpenRouter,
		`{"model":"deepseek/deepseek-v4-pro","stream":true,"max_tokens":1024,"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]}`},
	{"gemini_native/basic_text.upstream.sse", formatGemini, "gemini-3.1-pro-preview", providers.ProviderGoogle,
		`{"model":"gemini-3.1-pro-preview","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Say hi."}]}`},
	{"gemini_native/toolcall.upstream.sse", formatGemini, "gemini-3.1-pro-preview", providers.ProviderGoogle,
		`{"model":"gemini-3.1-pro-preview","stream":true,"max_tokens":1024,"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]}`},
	{"responses/toolcall.upstream.sse", formatOpenAIResponses, "gpt-5.5", providers.ProviderOpenAI,
		`{"model":"gpt-5.5","stream":true,"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":24576},"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]}`},
}

const weatherTool = `[{"name":"get_weather","description":"Get the weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`

func main() {
	logconfig.Configure()
	if os.Getenv("RECORD") != "1" {
		fmt.Println("Refusing to hit live providers without RECORD=1 (see file header for usage)")
		return
	}
	client := &http.Client{Timeout: 180 * time.Second}
	var recorded, skipped, failed int
	for _, c := range cases {
		key, keyEnv := apiKeyFor(c.format)
		if key == "" {
			fmt.Printf("SKIP  %s (%s not set)\n", c.fixture, keyEnv)
			skipped++
			continue
		}
		if err := record(client, c, key); err != nil {
			fmt.Printf("FAIL  %s: %v\n", c.fixture, err)
			failed++
			continue
		}
		fmt.Printf("OK    %s\n", c.fixture)
		recorded++
	}
	fmt.Printf("\nrecorded=%d skipped=%d failed=%d\n", recorded, skipped, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func record(client *http.Client, c recordCase, apiKey string) (err error) {
	env, err := translate.ParseAnthropic([]byte(c.anthropicBody))
	if err != nil {
		return fmt.Errorf("parse anthropic body: %w", err)
	}
	opts := translate.EmitOptions{
		TargetModel:    c.model,
		TargetProvider: c.provider,
		Capabilities:   router.Lookup(c.model),
	}

	prep, err := prepare(env, c.format, opts)
	if err != nil {
		return fmt.Errorf("emit upstream request: %w", err)
	}

	url, hdr := endpoint(c, apiKey)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d: %s", resp.StatusCode, observability.Preview(string(body), 400))
	}

	path := filepath.Join(fixtureRoot, c.fixture)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func prepare(env *translate.RequestEnvelope, f format, opts translate.EmitOptions) (providers.PreparedRequest, error) {
	switch f {
	case formatOpenAIChat:
		return env.PrepareOpenAI(http.Header{}, opts)
	case formatOpenAIResponses:
		return env.PrepareOpenAIResponses(http.Header{}, opts)
	case formatGemini:
		return env.PrepareGemini(http.Header{}, opts)
	case formatAnthropic:
		return env.PrepareAnthropic(http.Header{}, opts)
	default:
		return providers.PreparedRequest{}, fmt.Errorf("unknown format %d", f)
	}
}

func endpoint(c recordCase, apiKey string) (string, map[string]string) {
	switch c.format {
	case formatOpenAIChat:
		if c.provider == providers.ProviderOpenRouter {
			return "https://openrouter.ai/api/v1/chat/completions", map[string]string{"Authorization": "Bearer " + apiKey}
		}
		return "https://api.openai.com/v1/chat/completions", map[string]string{"Authorization": "Bearer " + apiKey}
	case formatOpenAIResponses:
		return "https://api.openai.com/v1/responses", map[string]string{"Authorization": "Bearer " + apiKey}
	case formatGemini:
		return "https://generativelanguage.googleapis.com/v1beta/models/" + c.model + ":streamGenerateContent?alt=sse",
			map[string]string{"x-goog-api-key": apiKey}
	case formatAnthropic:
		return "https://api.anthropic.com/v1/messages",
			map[string]string{"x-api-key": apiKey, "anthropic-version": "2023-06-01"}
	default:
		return "", nil
	}
}

func apiKeyFor(f format) (key, env string) {
	switch f {
	case formatOpenAIChat:
		return os.Getenv("OPENROUTER_API_KEY"), "OPENROUTER_API_KEY"
	case formatOpenAIResponses:
		return os.Getenv("OPENAI_API_KEY"), "OPENAI_API_KEY"
	case formatGemini:
		return os.Getenv("GOOGLE_API_KEY"), "GOOGLE_API_KEY"
	case formatAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY"), "ANTHROPIC_API_KEY"
	default:
		return "", ""
	}
}
