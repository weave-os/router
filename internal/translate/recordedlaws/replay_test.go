package recordedlaws_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	replayPrompt     = "Continue the task."
	replayToolOutput = "synthetic tool output"
)

type replayRequest struct {
	format wireFormat
	body   []byte
}

func toolDeclarations(tr turn, render func(name string) any) []any {
	seen := map[string]bool{}
	var out []any
	for _, tc := range tr.tools {
		if !seen[tc.name] {
			seen[tc.name] = true
			out = append(out, render(tc.name))
		}
	}
	return out
}

func anthropicReplay(client []byte, tr turn) replayRequest {
	msg := gjson.ParseBytes(client)
	follow := []any{}
	for _, tc := range msg.Get(`content.#(type=="tool_use")#`).Array() {
		follow = append(follow, map[string]any{"type": "tool_result", "tool_use_id": tc.Get("id").String(), "content": replayToolOutput})
	}
	if len(follow) == 0 {
		follow = append(follow, map[string]any{"type": "text", "text": "Thanks."})
	}
	tools := toolDeclarations(tr, func(name string) any {
		return map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}}
	})
	body := map[string]any{"model": clientModel, "max_tokens": 1024, "messages": []any{
		map[string]any{"role": "user", "content": replayPrompt},
		map[string]any{"role": "assistant", "content": json.RawMessage(msg.Get("content").Raw)},
		map[string]any{"role": "user", "content": follow},
	}}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return replayRequest{format: anthropicReq, body: mustMarshal(body)}
}

func chatReplay(client []byte, tr turn) replayRequest {
	msg := gjson.GetBytes(client, "choices.0.message")
	messages := []any{map[string]any{"role": "user", "content": replayPrompt}, json.RawMessage(msg.Raw)}
	for _, tc := range msg.Get("tool_calls").Array() {
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": tc.Get("id").String(), "content": replayToolOutput})
	}
	if len(msg.Get("tool_calls").Array()) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": "Thanks."})
	}
	body := map[string]any{"model": clientModel, "max_tokens": 1024, "messages": messages}
	if tools := toolDeclarations(tr, func(name string) any {
		return map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": map[string]any{"type": "object"}}}
	}); len(tools) > 0 {
		body["tools"] = tools
	}
	return replayRequest{format: chatReq, body: mustMarshal(body)}
}

func responsesReplay(client []byte, tr turn) replayRequest {
	input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": replayPrompt}}}}
	var outputs []any
	for _, item := range gjson.GetBytes(client, "output").Array() {
		input = append(input, json.RawMessage(item.Raw))
		if item.Get("type").String() == "function_call" {
			outputs = append(outputs, map[string]any{"type": "function_call_output", "call_id": item.Get("call_id").String(), "output": replayToolOutput})
		}
	}
	input = append(input, outputs...)
	if len(outputs) == 0 {
		input = append(input, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Thanks."}}})
	}
	body := map[string]any{"model": clientModel, "input": input}
	if tools := toolDeclarations(tr, func(name string) any {
		return map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object"}}
	}); len(tools) > 0 {
		body["tools"] = tools
	}
	return replayRequest{format: responsesReq, body: mustMarshal(body)}
}

func clientReplay(clientFormat wireFormat, client []byte, tr turn) replayRequest {
	assembled := assemble(clientFormat, client)
	switch responseFamily(clientFormat) {
	case "anthropic":
		return anthropicReplay(assembled, tr)
	case "chat":
		return chatReplay(assembled, tr)
	}
	return responsesReplay(assembled, tr)
}

func upstreamRequestTarget(upstream wireFormat) (wireFormat, string) {
	switch responseFamily(upstream) {
	case "anthropic":
		return anthropicReq, ""
	case "gemini":
		return geminiReq, geminiSignedTarget
	case "chat":
		return chatReq, ""
	}
	return responsesReq, ""
}

type emittedReplay struct {
	emitter string
	format  wireFormat
	body    []byte
	facts   facts
}

func emitReplay(hops []requestHop, req replayRequest, upstream wireFormat) emittedReplay {
	target, via := upstreamRequestTarget(upstream)
	body, from := req.body, req.format
	if from == responsesReq {
		converted, err := convertResponses(body)
		if err != nil {
			return emittedReplay{facts: facts{"error": err.Error()}}
		}
		body, from = converted, chatReq
	}
	h := findRequestHop(hops, from, target, via)
	out, err := h.emit(body)
	if err != nil {
		return emittedReplay{emitter: h.name(), facts: facts{"error": err.Error()}}
	}
	return emittedReplay{emitter: h.name(), format: target, body: out, facts: assistantReplayFacts(requestFacts(target, out))}
}

func emittedCallIDProblems(format wireFormat, body []byte) []string {
	var p problems
	if format != chatReq && format != responsesReq {
		return p
	}
	for _, tc := range requestConversation(format, body).messages {
		for _, call := range tc.turn.tools {
			p.require(len(call.id) <= 64, "emitted call id %q is longer than 64 characters", call.id)
		}
		for _, result := range tc.results {
			p.require(len(result.id) <= 64, "emitted result call id %q is longer than 64 characters", result.id)
		}
	}
	return p
}

func (e emittedReplay) problems() []string {
	if e.body == nil {
		return nil
	}
	return append(validateRequest(e.format, e.body), emittedCallIDProblems(e.format, e.body)...)
}

func assistantReplayFacts(all facts) facts {
	out := facts{}
	for k, v := range all {
		if rest, ok := strings.CutPrefix(k, "msg.1."); ok && rest != "role" {
			out[rest] = v
		}
	}
	return out
}

func recordedTurnFacts(tr turn) facts {
	tr.stop, tr.usage = "", nil
	return tr.facts()
}

func TestRecordedTurnsReplayIntoTheNextRequest(t *testing.T) {
	ledger := newLossLedger(replayLossAllowlist())
	hops := recordedTargets(t).requestHops()
	for _, rec := range responseCorpus(t) {
		upstream := turnOf(rec.format, rec.body)
		want := recordedTurnFacts(upstream)
		for _, h := range responseHops() {
			if h.from != rec.format {
				continue
			}
			t.Run(string(rec.origin)+"/"+rec.name+"/"+h.name(), func(t *testing.T) {
				req := clientReplay(h.to, h.run(t, rec.body), upstream)
				for _, problem := range validateClientReplay(req.format, req.body) {
					t.Errorf("client replay request is not valid %s: %s", req.format, problem)
				}
				emitted := emitReplay(hops, req, rec.format)
				name := "replay:" + h.name() + "+" + emitted.emitter
				for _, problem := range emitted.problems() {
					t.Errorf("%s emits an invalid %s request: %s", name, emitted.format, problem)
				}
				for _, d := range ledger.unexplained(name, diffFacts(want, emitted.facts)) {
					t.Errorf("%s does not return the recorded turn: %s", name, d)
				}
			})
		}
	}
	require.NotEmpty(t, hops)
	for _, r := range ledger.stale() {
		t.Errorf("allowlisted replay loss no longer happens; delete it: %s", r)
	}
}
