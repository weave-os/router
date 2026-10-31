package translate

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesItemSnapshot keeps the streamed output_item.done items so an empty
// terminal response.output can be rebuilt. The Codex backend sends an empty
// snapshot, and SDK clients that read their answer from it report no response.
type ResponsesItemSnapshot struct {
	items map[int64]json.RawMessage
}

// Observe records event when it is an output_item.done carrying an output_index.
func (s *ResponsesItemSnapshot) Observe(event gjson.Result) {
	if event.Get("type").Str != string(responsesOutputItemDone) {
		return
	}
	index := event.Get("output_index")
	item := event.Get("item")
	if index.Type != gjson.Number || !item.IsObject() {
		return
	}
	if s.items == nil {
		s.items = map[int64]json.RawMessage{}
	}
	s.items[index.Int()] = json.RawMessage(item.Raw)
}

// Backfill sets the empty array at path in payload to the observed items, in
// output_index order. A populated or missing array is left untouched.
func (s *ResponsesItemSnapshot) Backfill(payload []byte, path string) ([]byte, bool) {
	output := gjson.GetBytes(payload, path)
	if len(s.items) == 0 || !output.IsArray() || len(output.Array()) > 0 {
		return payload, false
	}
	indexes := slices.Sorted(maps.Keys(s.items))
	items := make([]string, 0, len(indexes))
	for _, index := range indexes {
		items = append(items, string(s.items[index]))
	}
	backfilled, err := sjson.SetRawBytes(payload, path, []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return payload, false
	}
	return backfilled, true
}

// Reset forgets every observed item.
func (s *ResponsesItemSnapshot) Reset() {
	s.items = nil
}
