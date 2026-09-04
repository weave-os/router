package analytics_test

import (
	"reflect"
	"strings"
	"testing"

	"weave-os/router/internal/analytics"

	"github.com/stretchr/testify/require"
)

// The schema endpoint is the machine-readable contract; a Decision field without
// a Schema entry would ship undocumented.
func TestSchemaMatchesDecisionWireShape(t *testing.T) {
	decision := reflect.TypeOf(analytics.Decision{})
	names := make([]string, 0, decision.NumField())
	for i := range decision.NumField() {
		tag := decision.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag, "Decision.%s has no json tag", decision.Field(i).Name)
		names = append(names, strings.Split(tag, ",")[0])
	}

	schema := analytics.Schema()
	documented := make([]string, 0, len(schema))
	for _, f := range schema {
		documented = append(documented, f.Name)
	}

	require.Equal(t, names, documented, "Schema() must list every Decision field, in wire order")
}

func TestSchemaFieldsAreDocumented(t *testing.T) {
	for _, f := range analytics.Schema() {
		require.NotEmpty(t, f.Description, "%s has no description", f.Name)
		require.Contains(t, []string{"string", "timestamp", "integer", "float", "boolean", "string[]"}, f.Type, "%s has an unknown type", f.Name)
	}
}

// decision_reason ships verbatim and can carry version-specific internals, so
// the dictionary has to warn consumers off parsing it.
func TestDecisionReasonDocumentedAsUnstable(t *testing.T) {
	for _, f := range analytics.Schema() {
		if f.Name != "decision_reason" {
			continue
		}
		require.Contains(t, f.Description, "not parse")
		return
	}
	t.Fatal("decision_reason missing from schema")
}
