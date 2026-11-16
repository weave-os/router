package translate

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ResponsesItemSnapshot struct {
	items map[int64]json.RawMessage
}

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

func (s *ResponsesItemSnapshot) Reset() {
	s.items = nil
}
