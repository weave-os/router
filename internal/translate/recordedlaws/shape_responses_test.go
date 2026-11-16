package recordedlaws_test

import (
	"github.com/tidwall/gjson"
)

var responsesStatuses = map[string]bool{"completed": true, "incomplete": true, "failed": true}

func validateResponsesItem(p *problems, i int, item gjson.Result) {
	switch kind := item.Get("type").String(); kind {
	case "message":
		p.require(item.Get("role").String() == "assistant", "output[%d] message role is %q", i, item.Get("role").String())
		for j, part := range item.Get("content").Array() {
			p.require(part.Get("type").String() == "output_text" && part.Get("text").Type == gjson.String, "output[%d].content[%d] is not output_text", i, j)
		}
	case "function_call":
		p.require(item.Get("call_id").String() != "", "output[%d] function_call has no call_id", i)
		p.require(len(item.Get("call_id").String()) <= 64, "output[%d] call_id is longer than 64 characters", i)
		p.require(item.Get("name").String() != "", "output[%d] function_call has no name", i)
		p.require(gjson.Parse(item.Get("arguments").String()).IsObject(), "output[%d] arguments are not a JSON object string", i)
	case "reasoning":
		p.require(item.Get("summary").IsArray(), "output[%d] reasoning has no summary array", i)
	default:
		p.addf("output[%d] has unexpected item type %q", i, kind)
	}
}

func validateResponsesObject(resp gjson.Result) []string {
	var p problems
	p.require(resp.Get("object").String() == "response", "object is %q, want response", resp.Get("object").String())
	p.require(resp.Get("id").String() != "", "response has no id")
	p.require(responsesStatuses[resp.Get("status").String()], "status %q is not terminal", resp.Get("status").String())
	p.require(resp.Get("output").IsArray(), "output is not an array")
	for i, item := range resp.Get("output").Array() {
		validateResponsesItem(&p, i, item)
	}
	u := resp.Get("usage")
	in, out, total := u.Get("input_tokens"), u.Get("output_tokens"), u.Get("total_tokens")
	p.require(in.Type == gjson.Number && out.Type == gjson.Number, "usage lacks input_tokens/output_tokens")
	p.require(!total.Exists() || total.Int() == in.Int()+out.Int(), "usage.total_tokens %d != input %d + output %d", total.Int(), in.Int(), out.Int())
	p.require(u.Get("input_tokens_details.cached_tokens").Int() <= in.Int(), "cached_tokens exceed input_tokens")
	return p
}

func validateResponsesTool(tool gjson.Result) []string {
	var p problems
	p.require(tool.Get("type").String() == "function", "tool type is %q, want function", tool.Get("type").String())
	p.require(tool.Get("name").String() != "", "tool has no name")
	p.require(tool.Get("parameters").IsObject(), "tool parameters are not an object")
	return p
}

type responsesStreamState struct {
	p         problems
	lastSeq   int64
	started   bool
	terminal  bool
	added     map[int64]string
	completed map[int64]bool
	text      map[string]string
}

func (s *responsesStreamState) itemEvent(i int, kind string, data gjson.Result) {
	idx := data.Get("output_index").Int()
	switch kind {
	case "response.output_item.added":
		_, dup := s.added[idx]
		s.p.require(!dup, "event %d re-adds output_index %d", i, idx)
		s.added[idx] = data.Get("item.id").String()
	case "response.output_item.done":
		_, ok := s.added[idx]
		s.p.require(ok, "event %d finishes output_index %d that was never added", i, idx)
		s.p.require(data.Get("item.id").String() == s.added[idx], "event %d finishes item %q, added as %q", i, data.Get("item.id").String(), s.added[idx])
		s.completed[idx] = true
		validateResponsesItem(&s.p, int(idx), data.Get("item"))
	default:
		id, ok := s.added[idx]
		s.p.require(ok && !s.completed[idx], "event %d (%s) targets output_index %d outside its added/done window", i, kind, idx)
		s.p.require(!data.Get("item_id").Exists() || data.Get("item_id").String() == id, "event %d item_id %q, want %q", i, data.Get("item_id").String(), id)
	}
	if kind == "response.output_text.delta" {
		s.text[data.Get("item_id").String()] += data.Get("delta").String()
	}
	if kind == "response.output_text.done" {
		s.p.require(s.text[data.Get("item_id").String()] == data.Get("text").String(), "event %d output_text.done text differs from its deltas", i)
	}
}

func (s *responsesStreamState) event(i int, f sseFrame) {
	data := gjson.Parse(f.data)
	kind := data.Get("type").String()
	s.p.require(f.event == kind, "event %d name %q carries type %q", i, f.event, kind)
	s.p.require(!s.terminal, "event %d (%s) after the terminal event", i, kind)
	seq := data.Get("sequence_number")
	s.p.require(seq.Type == gjson.Number && seq.Int() > s.lastSeq, "event %d sequence_number %s does not increase past %d", i, seq.Raw, s.lastSeq)
	s.lastSeq = seq.Int()
	if !s.started {
		s.p.require(kind == "response.created", "first event is %q, want response.created", kind)
		s.started = true
		return
	}
	switch {
	case responsesTerminalEvents[kind]:
		s.terminal = true
		for _, problem := range validateResponsesObject(data.Get("response")) {
			s.p.addf("terminal response: %s", problem)
		}
	case kind == "response.in_progress":
	case data.Get("output_index").Exists():
		s.itemEvent(i, kind, data)
	default:
		s.p.addf("event %d has unexpected type %q", i, kind)
	}
}

func validateResponsesStream(body []byte) []string {
	s := &responsesStreamState{lastSeq: -1, added: map[int64]string{}, completed: map[int64]bool{}, text: map[string]string{}}
	for i, f := range parseSSE(body) {
		s.event(i, f)
	}
	s.p.require(s.terminal, "stream never sends a terminal response event")
	for idx := range s.added {
		s.p.require(s.completed[idx], "output_index %d is never finished", idx)
	}
	return s.p
}
