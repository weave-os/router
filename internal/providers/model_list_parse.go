package providers

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// ErrUnknownModelListShape is returned when a model-list body matches neither
// the OpenAI/Anthropic list shape nor a gateway's bare model array.
var ErrUnknownModelListShape = errors.New("model listing response has no recognizable model list")

// maxModelListErrorBytes caps how much of a rejected model-list body is quoted
// back in the error.
const maxModelListErrorBytes = 400

// ModelListStatusError describes a rejected model-list response, quoting a
// truncated, single-line excerpt of the upstream body. Gateways explain
// themselves there (Snowflake Cortex answers 400 with a JSON "message"), and
// without it a status code alone is undiagnosable in production.
func ModelListStatusError(status int, body []byte) error {
	detail := gjson.GetBytes(body, "message").String()
	if detail == "" {
		detail = gjson.GetBytes(body, "error.message").String()
	}
	if detail == "" {
		detail = string(body)
	}
	detail = strings.TrimSpace(strings.Join(strings.Fields(detail), " "))
	if len(detail) > maxModelListErrorBytes {
		detail = detail[:maxModelListErrorBytes] + "…"
	}
	if detail == "" {
		return fmt.Errorf("model listing returned status %d", status)
	}
	return fmt.Errorf("model listing returned status %d: %s", status, detail)
}

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
