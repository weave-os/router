package providers

import (
	"errors"
	"sort"

	"github.com/tidwall/gjson"
)

// ErrUnknownModelListShape is returned when a model-list body matches neither
// the OpenAI/Anthropic list shape nor a gateway's bare model array.
var ErrUnknownModelListShape = errors.New("model listing response has no recognizable model list")

// ParseModelIDs extracts sorted, deduplicated model IDs from a model-list body.
// It accepts the OpenAI/Anthropic shape ({"data":[{"id":...}]}) and the bare
// array gateways publish instead, e.g. Snowflake Cortex's {"models":["..."]},
// whose entries may be plain strings or objects keyed by id/name/model.
func ParseModelIDs(body []byte) ([]string, error) {
	entries := gjson.GetBytes(body, "data")
	if !entries.IsArray() {
		entries = gjson.GetBytes(body, "models")
	}
	if !entries.IsArray() {
		return nil, ErrUnknownModelListShape
	}

	seen := make(map[string]struct{})
	var ids []string
	for _, entry := range entries.Array() {
		id := modelIDFrom(entry)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func modelIDFrom(entry gjson.Result) string {
	if entry.Type == gjson.String {
		return entry.String()
	}
	for _, field := range []string{"id", "name", "model"} {
		if value := entry.Get(field).String(); value != "" {
			return value
		}
	}
	return ""
}
