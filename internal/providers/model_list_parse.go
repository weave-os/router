package providers

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tidwall/gjson"
)

// ErrUnknownModelListShape is returned when a model-list body matches neither
// the OpenAI/Anthropic list shape nor a gateway's bare model array.
var ErrUnknownModelListShape = errors.New("model listing response has no recognizable model list")

// ModelListFailureCategory is the non-sensitive class of a model-list failure.
type ModelListFailureCategory string

const (
	// ModelListFailureUpstreamStatus means the endpoint rejected the request.
	ModelListFailureUpstreamStatus ModelListFailureCategory = "upstream_status"
)

// ModelListHTTPStatusError describes an upstream rejection without retaining
// response content that may contain credentials or private endpoint details.
type ModelListHTTPStatusError struct {
	Status   int
	Category ModelListFailureCategory
}

func (e *ModelListHTTPStatusError) Error() string {
	return fmt.Sprintf("model listing failed with status %d (%s)", e.Status, e.Category)
}

// NewModelListStatusError returns a safe typed upstream-status failure.
func NewModelListStatusError(status int) error {
	return &ModelListHTTPStatusError{Status: status, Category: ModelListFailureUpstreamStatus}
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
