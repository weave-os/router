package recordedlaws_test

import (
	"github.com/tidwall/gjson"
)

var chatFinishReasons = map[string]bool{"stop": true, "length": true, "tool_calls": true, "content_filter": true}

func validateChatToolCall(p *problems, i int, tc gjson.Result) {
	p.require(tc.Get("id").String() != "", "tool_calls[%d] has no id", i)
	p.require(tc.Get("type").String() == "function", "tool_calls[%d] type is %q, want function", i, tc.Get("type").String())
	p.require(tc.Get("function.name").String() != "", "tool_calls[%d] has no function.name", i)
	args := tc.Get("function.arguments")
	p.require(args.Type == gjson.String && gjson.Parse(args.String()).IsObject(), "tool_calls[%d] arguments %q are not a JSON object string", i, args.String())
}

func validateChatUsage(p *problems, u gjson.Result) {
	prompt, completion, total := u.Get("prompt_tokens"), u.Get("completion_tokens"), u.Get("total_tokens")
	p.require(prompt.Type == gjson.Number && completion.Type == gjson.Number, "usage lacks prompt_tokens/completion_tokens")
	p.require(!total.Exists() || total.Int() == prompt.Int()+completion.Int(), "usage.total_tokens %d != prompt %d + completion %d", total.Int(), prompt.Int(), completion.Int())
	cached := u.Get("prompt_tokens_details.cached_tokens")
	p.require(!cached.Exists() || cached.Int() <= prompt.Int(), "cached_tokens %d exceed prompt_tokens %d", cached.Int(), prompt.Int())
}

func validateChatCompletion(body gjson.Result) []string {
	var p problems
	p.require(body.Get("object").String() == "chat.completion", "object is %q, want chat.completion", body.Get("object").String())
	p.require(body.Get("id").String() != "", "completion has no id")
	msg := body.Get("choices.0.message")
	p.require(msg.Get("role").String() == "assistant", "choices[0].message.role is %q", msg.Get("role").String())
	content := msg.Get("content")
	p.require(content.Type == gjson.String || content.Type == gjson.Null, "message.content is neither string nor null")
	calls := msg.Get("tool_calls").Array()
	for i, tc := range calls {
		validateChatToolCall(&p, i, tc)
	}
	finish := body.Get("choices.0.finish_reason").String()
	p.require(chatFinishReasons[finish], "finish_reason %q is not a documented value", finish)
	p.require(len(calls) == 0 || finish == "tool_calls", "tool calls end with finish_reason %q, want tool_calls", finish)
	validateChatUsage(&p, body.Get("usage"))
	return p
}

type chatStreamState struct {
	p        problems
	id       string
	done     bool
	finish   string
	toolIDs  map[int64]string
	toolArgs map[int64]string
}

func (s *chatStreamState) toolDelta(i int, tc gjson.Result) {
	idx := tc.Get("index")
	s.p.require(idx.Type == gjson.Number, "chunk %d tool call delta has no index", i)
	id, seen := s.toolIDs[idx.Int()]
	switch {
	case !seen:
		s.p.require(tc.Get("id").String() != "" && tc.Get("function.name").String() != "", "chunk %d opens tool call %d without id and name", i, idx.Int())
		s.toolIDs[idx.Int()] = tc.Get("id").String()
	case tc.Get("id").Exists():
		s.p.require(tc.Get("id").String() == id, "chunk %d changes tool call %d id", i, idx.Int())
	}
	s.toolArgs[idx.Int()] += tc.Get("function.arguments").String()
}

func (s *chatStreamState) chunk(i int, data gjson.Result) {
	s.p.require(data.Get("object").String() == "chat.completion.chunk", "chunk %d object is %q", i, data.Get("object").String())
	if s.id == "" {
		s.id = data.Get("id").String()
	}
	s.p.require(data.Get("id").String() == s.id && s.id != "", "chunk %d id %q differs from %q", i, data.Get("id").String(), s.id)
	for _, tc := range data.Get("choices.0.delta.tool_calls").Array() {
		s.p.require(s.finish == "", "chunk %d streams a tool call after finish_reason", i)
		s.toolDelta(i, tc)
	}
	if fr := data.Get("choices.0.finish_reason"); fr.Exists() && fr.Type != gjson.Null {
		s.p.require(s.finish == "", "chunk %d sends a second finish_reason", i)
		s.finish = fr.String()
	}
	if u := data.Get("usage"); u.IsObject() {
		validateChatUsage(&s.p, u)
	}
}

func validateChatStream(body []byte) []string {
	s := &chatStreamState{toolIDs: map[int64]string{}, toolArgs: map[int64]string{}}
	for i, f := range parseSSE(body) {
		s.p.require(!s.done, "frame %d arrives after [DONE]", i)
		if f.data == "[DONE]" {
			s.done = true
			continue
		}
		s.p.require(gjson.Valid(f.data), "frame %d is not JSON", i)
		s.chunk(i, gjson.Parse(f.data))
	}
	s.p.require(s.done, "stream never sends [DONE]")
	s.p.require(chatFinishReasons[s.finish], "finish_reason %q is not a documented value", s.finish)
	s.p.require(len(s.toolIDs) == 0 || s.finish == "tool_calls", "tool calls end with finish_reason %q, want tool_calls", s.finish)
	for idx, args := range s.toolArgs {
		s.p.require(gjson.Parse(args).IsObject(), "tool call %d streams arguments %q that are not a JSON object", idx, args)
	}
	return s.p
}
