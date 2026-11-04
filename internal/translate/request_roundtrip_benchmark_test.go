package translate_test

import (
	"bytes"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// hotPathAnthropicRequest is an agentic Anthropic Messages turn: a system
// prompt, tool definitions and a completed tool call, the shape coding
// clients send on most turns.
var hotPathAnthropicRequest = []byte(`{
	"model": "claude-sonnet-4-5",
	"max_tokens": 8192,
	"stream": true,
	"system": [{"type": "text", "text": "You are a coding assistant working in a Go repository."}],
	"tools": [
		{"name": "read_file", "description": "Read a file from the workspace.", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}},
		{"name": "run_command", "description": "Run a shell command.", "input_schema": {"type": "object", "properties": {"command": {"type": "string"}, "timeout_ms": {"type": "integer"}}, "required": ["command"]}}
	],
	"messages": [
		{"role": "user", "content": [{"type": "text", "text": "Why does the build fail? Read main.go first."}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "I will read main.go."},
			{"type": "tool_use", "id": "toolu_bench_1", "name": "read_file", "input": {"path": "main.go"}}
		]},
		{"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "toolu_bench_1", "content": "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"}
		]},
		{"role": "assistant", "content": [{"type": "text", "text": "main.go uses fmt without importing it."}]},
		{"role": "user", "content": [{"type": "text", "text": "Fix it and run the tests."}]}
	]
}`)

type hotPathHop struct {
	name    string
	parse   func([]byte) (*translate.RequestEnvelope, error)
	prepare func(*translate.RequestEnvelope) (providers.PreparedRequest, error)
	// marker is a target-format field proving the body was really re-emitted.
	marker string
}

var (
	hopToOpenAI = hotPathHop{
		name:   "openai",
		parse:  translate.ParseOpenAI,
		marker: `"tool_calls"`,
		prepare: func(e *translate.RequestEnvelope) (providers.PreparedRequest, error) {
			return e.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "gpt-4.1"})
		},
	}
	hopToGemini = hotPathHop{
		name:   "gemini",
		parse:  translate.ParseGemini,
		marker: `"functionCall"`,
		prepare: func(e *translate.RequestEnvelope) (providers.PreparedRequest, error) {
			return e.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-flash"})
		},
	}
	hopToAnthropic = hotPathHop{
		name:   "anthropic",
		parse:  translate.ParseAnthropic,
		marker: `"tool_use"`,
		prepare: func(e *translate.RequestEnvelope) (providers.PreparedRequest, error) {
			return e.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-sonnet-4-5"})
		},
	}
)

// translateChain parses body with the first hop's parser, then emits each
// later hop's format from the previous hop's output.
func translateChain(body []byte, chain []hotPathHop) ([]byte, error) {
	for i := 1; i < len(chain); i++ {
		env, err := chain[i-1].parse(body)
		if err != nil {
			return nil, err
		}
		prepared, err := chain[i].prepare(env)
		if err != nil {
			return nil, err
		}
		body = prepared.Body
	}
	return body, nil
}

// BenchmarkHotPathRequestTranslation measures parse + emit for each request
// hop the proxy runs, and an Anthropic → OpenAI → Anthropic → Gemini chain.
// Gemini ingress is only emitted back to Gemini, so it has no cross-format hop.
func BenchmarkHotPathRequestTranslation(b *testing.B) {
	toOpenAI, err := translateChain(hotPathAnthropicRequest, []hotPathHop{hopToAnthropic, hopToOpenAI})
	if err != nil {
		b.Fatal(err)
	}
	toGemini, err := translateChain(hotPathAnthropicRequest, []hotPathHop{hopToAnthropic, hopToGemini})
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name  string
		input []byte
		chain []hotPathHop
	}{
		{"anthropic-to-openai", hotPathAnthropicRequest, []hotPathHop{hopToAnthropic, hopToOpenAI}},
		{"anthropic-to-gemini", hotPathAnthropicRequest, []hotPathHop{hopToAnthropic, hopToGemini}},
		{"openai-to-anthropic", toOpenAI, []hotPathHop{hopToOpenAI, hopToAnthropic}},
		{"openai-to-gemini", toOpenAI, []hotPathHop{hopToOpenAI, hopToGemini}},
		{"gemini-to-gemini", toGemini, []hotPathHop{hopToGemini, hopToGemini}},
		{"round-trip", hotPathAnthropicRequest, []hotPathHop{hopToAnthropic, hopToOpenAI, hopToAnthropic, hopToGemini}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			out, err := translateChain(tc.input, tc.chain)
			if err != nil {
				b.Fatal(err)
			}
			last := tc.chain[len(tc.chain)-1]
			if !bytes.Contains(out, []byte(last.marker)) || !bytes.Contains(out, []byte("read_file")) {
				b.Fatalf("%s output lost the tool call: %s", last.name, out)
			}
			b.SetBytes(int64(len(tc.input)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := translateChain(tc.input, tc.chain); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
