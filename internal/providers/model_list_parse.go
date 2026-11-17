package providers

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tidwall/gjson"
)

var ErrUnknownModelListShape = errors.New("model listing response has no recognizable model list")

type ModelListFailureCategory string

const (
	ModelListFailureUpstreamStatus ModelListFailureCategory = "upstream_status"
)

type ModelListHTTPStatusError struct {
	Status   int
	Category ModelListFailureCategory
}

func (e *ModelListHTTPStatusError) Error() string {
	return fmt.Sprintf("model listing failed with status %d (%s)", e.Status, e.Category)
}

func NewModelListStatusError(status int) error {
	return &ModelListHTTPStatusError{Status: status, Category: ModelListFailureUpstreamStatus}
}

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
