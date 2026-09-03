package translate_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"workweave/router/internal/router"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareGemini_SchemaFidelity(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		// wantDegraded expects the request to succeed with the tool's
		// declaration emitted WITHOUT parameters (the per-tool backstop for
		// schemas that stay unrepresentable after widening).
		wantDegraded bool
		check        func(*testing.T, map[string]any)
	}{
		{
			// Gemini types every enum member as TYPE_STRING; a numeric const lowers
			// to a numeric enum that 400s the request, so it gets dropped. toolcheck
			// still enforces const against the original schema.
			name:   "numeric const drops the unrepresentable enum but keeps the type",
			schema: `{"type":"number","const":7}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.NotContains(t, schema, "enum")
				assert.Equal(t, "number", schema["type"])
			},
		},
		{
			name:   "string const still lowers to an enum",
			schema: `{"type":"string","const":"go"}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, []any{"go"}, schema["enum"])
			},
		},
		{
			name:   "allOf merges disjoint object properties",
			schema: `{"allOf":[{"type":"object","properties":{"left":{"type":"string"}},"required":["left"]},{"type":"object","properties":{"right":{"type":"boolean"}},"required":["right"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Contains(t, schema["properties"].(map[string]any), "left")
				assert.Contains(t, schema["properties"].(map[string]any), "right")
				assert.ElementsMatch(t, []any{"left", "right"}, schema["required"])
			},
		},
		{
			// Tool schemas are generation hints; the client validates against the
			// ORIGINAL schema, so a widened superset is always safe.
			name:   "allOf conflict widens by dropping the conflicting branch",
			schema: `{"allOf":[{"type":"string"},{"type":"number"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
			},
		},
		{
			name:   "allOf annotation conflict merges with earlier branch winning",
			schema: `{"allOf":[{"type":"string","description":"from the base type"},{"type":"string","description":"per-tool override"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
				assert.Equal(t, "from the base type", schema["description"])
			},
		},
		{
			// A widened branch can orphan sibling `required` entries;
			// Gemini rejects required-without-property, so prune after widening.
			name:   "required is pruned when the branch that declared it is dropped",
			schema: `{"type":"object","required":["b"],"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"pattern":"^1"},{"type":"object","properties":{"b":{"type":"string"}},"pattern":"^2"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				props := schema["properties"].(map[string]any)
				assert.Contains(t, props, "a")
				assert.NotContains(t, props, "b")
				assert.NotContains(t, schema, "required")
			},
		},
		{
			// Annotation-only branch (prod-observed shape); outer branch value wins.
			name:   "allOf branches disagreeing on annotations merge",
			schema: `{"type":"object","properties":{"to":{"allOf":[{"type":"string","description":"A","title":"T1"},{"type":"string","description":"B","title":"T2"}]}}}`,
			check: func(t *testing.T, schema map[string]any) {
				to := schema["properties"].(map[string]any)["to"].(map[string]any)
				assert.Equal(t, "string", to["type"])
				assert.Equal(t, "A", to["description"])
				assert.Equal(t, "T1", to["title"])
			},
		},
		{
			name:   "allOf bounds narrow to the stricter of each pair",
			schema: `{"allOf":[{"type":"string","minLength":1,"maxLength":10},{"type":"string","minLength":5,"maxLength":8}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, float64(5), schema["minLength"])
				assert.Equal(t, float64(8), schema["maxLength"])
			},
		},
		{
			name:   "allOf numeric bounds narrow regardless of branch order",
			schema: `{"allOf":[{"type":"integer","minimum":10,"maximum":20},{"type":"integer","minimum":2,"maximum":50}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, float64(10), schema["minimum"])
				assert.Equal(t, float64(20), schema["maximum"])
			},
		},
		{
			name:   "allOf enums intersect to the shared members",
			schema: `{"allOf":[{"type":"string","enum":["a","b","c"]},{"type":"string","enum":["b","c","d"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, []any{"b", "c"}, schema["enum"])
			},
		},
		{
			// Disjoint enums admit no value at all — an unsatisfiable
			// intersection — so the conflicting branch is dropped (widened)
			// rather than failing the request.
			name:   "allOf disjoint enums widen by dropping the later branch",
			schema: `{"allOf":[{"type":"string","enum":["a"]},{"type":"string","enum":["b"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, []any{"a"}, schema["enum"])
			},
		},
		{
			// pattern is a constraint, not an annotation: identical branch
			// patterns merge; differing regexes cannot be intersected, so the
			// later branch is dropped (widened) instead.
			name:   "allOf identical patterns merge",
			schema: `{"allOf":[{"type":"string","pattern":"^[A-Z]{3}$"},{"type":"string","pattern":"^[A-Z]{3}$"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "^[A-Z]{3}$", schema["pattern"])
			},
		},
		{
			name:   "allOf differing patterns widen by dropping the later branch",
			schema: `{"allOf":[{"type":"string","pattern":"^[A-Z]+$"},{"type":"string","pattern":"^[A-Z]{3}$"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "^[A-Z]+$", schema["pattern"])
			},
		},
		{
			name:   "allOf intersects a property declared by both branches",
			schema: `{"allOf":[{"type":"object","properties":{"id":{"type":"string","minLength":1}}},{"type":"object","properties":{"id":{"type":"string","minLength":4}}}]}`,
			check: func(t *testing.T, schema map[string]any) {
				id := schema["properties"].(map[string]any)["id"].(map[string]any)
				assert.Equal(t, "string", id["type"])
				assert.Equal(t, float64(4), id["minLength"])
			},
		},
		{
			// A type conflict nested under a shared property makes the later
			// branch unmergeable, so it is dropped (widened).
			name:   "allOf conflict nested under a shared property widens",
			schema: `{"allOf":[{"type":"object","properties":{"id":{"type":"string"}}},{"type":"object","properties":{"id":{"type":"number"}}}]}`,
			check: func(t *testing.T, schema map[string]any) {
				id := schema["properties"].(map[string]any)["id"].(map[string]any)
				assert.Equal(t, "string", id["type"])
			},
		},
		{
			name:   "allOf array item schemas intersect",
			schema: `{"allOf":[{"type":"array","items":{"type":"string","minLength":2}},{"type":"array","items":{"type":"string","minLength":6}}]}`,
			check: func(t *testing.T, schema map[string]any) {
				items := schema["items"].(map[string]any)
				assert.Equal(t, "string", items["type"])
				assert.Equal(t, float64(6), items["minLength"])
			},
		},
		{
			// nullable intersects: a nullable branch AND a non-nullable one is
			// non-nullable, which Gemini spells as the absence of the key.
			name:   "allOf nullable requires both branches to admit null",
			schema: `{"allOf":[{"type":["string","null"]},{"type":"string"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
				assert.NotContains(t, schema, "nullable")
			},
		},
		{
			name:   "allOf keeps nullable when both branches admit null",
			schema: `{"allOf":[{"type":["string","null"]},{"type":["string","null"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
				assert.Equal(t, true, schema["nullable"])
			},
		},
		{
			// A typed sibling that omits nullable is non-nullable, so a nullable
			// allOf branch must not widen it back (regression: intersect used to
			// absorb nullable:true from the branch over the sibling's implicit
			// non-nullability).
			name:   "nullable allOf branch does not widen a non-null sibling",
			schema: `{"type":"string","allOf":[{"type":["string","null"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
				assert.NotContains(t, schema, "nullable")
			},
		},
		{
			// A NESTED sibling property may still carry a raw type:[T,"null"]
			// array when it reaches intersection (only the top level is
			// normalized before the sibling merge); it must intersect with the
			// branch's constraints, not fail into the lenient path and drop them.
			name:   "raw nullable type array on a nested sibling property still intersects",
			schema: `{"type":"object","properties":{"id":{"type":["string","null"]}},"allOf":[{"type":"object","properties":{"id":{"type":"string","minLength":4}}}]}`,
			check: func(t *testing.T, schema map[string]any) {
				id := schema["properties"].(map[string]any)["id"].(map[string]any)
				assert.Equal(t, "string", id["type"])
				assert.Equal(t, float64(4), id["minLength"])
				assert.NotContains(t, id, "nullable")
			},
		},
		{
			// A nullable sibling combined with an identical nullable branch
			// stays nullable rather than misreading the raw sibling type array as
			// non-null and rejecting the pair as conflicting.
			name:   "nullable sibling plus same nullable branch merges",
			schema: `{"type":["string","null"],"allOf":[{"type":["string","null"]}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Equal(t, "string", schema["type"])
				assert.Equal(t, true, schema["nullable"])
			},
		},
		{
			name:   "anyOf preserves every branch",
			schema: `{"anyOf":[{"type":"string"},{"type":"number"}]}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.Len(t, schema["anyOf"], 2)
			},
		},
		{
			// oneOf has no safe widening path — per-tool backstop degrades
			// to no parameters rather than silently narrowing the input language.
			name:         "oneOf degrades the tool rather than selecting a branch",
			schema:       `{"oneOf":[{"type":"string"},{"type":"number"}]}`,
			wantDegraded: true,
		},
		{
			name:         "unresolved reference degrades the tool",
			schema:       `{"$ref":"#/$defs/Missing","$defs":{"Present":{"type":"string"}}}`,
			wantDegraded: true,
		},
		{
			// additionalProperties has no Gemini equivalent (Gemini always
			// behaves as if it were disallowed) and toolcheck validates
			// emitted tool calls against the original inbound schema, not
			// this sanitized one — so it's dropped, not rejected. See
			// TestPrepareGemini_StripsJSONSchemaFieldsGoogleRejects for the
			// prod-observed case (Claude Code's Agent/Task tool schema).
			name:   "additionalProperties is dropped, not rejected",
			schema: `{"type":"object","additionalProperties":false}`,
			check: func(t *testing.T, schema map[string]any) {
				assert.NotContains(t, schema, "additionalProperties")
				assert.Equal(t, "object", schema["type"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"tool","input_schema":` + tt.schema + `}]}`)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			declaration := out["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
			if tt.wantDegraded {
				assert.NotContains(t, declaration, "parameters")
				return
			}
			tt.check(t, declaration["parameters"].(map[string]any))
		})
	}
}

func TestPrepareGemini_DeduplicatesFunctionDeclarations(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		parse       func([]byte) (*translate.RequestEnvelope, error)
		wantErr     error
		declaration func(map[string]any) []any
	}{
		{
			name:  "OpenAI identical duplicates collapse",
			body:  []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"read","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","description":"read","parameters":{"type":"object"}}}]}`),
			parse: translate.ParseOpenAI,
			declaration: func(out map[string]any) []any {
				return out["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
			},
		},
		{
			name:    "Anthropic conflicting duplicates reject",
			body:    []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","description":"one","input_schema":{"type":"object"}},{"name":"read","description":"two","input_schema":{"type":"object"}}]}`),
			parse:   translate.ParseAnthropic,
			wantErr: translate.ErrGeminiToolDeclarationConflict,
		},
		{
			name:  "Gemini identical duplicates collapse",
			body:  []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"read","description":"read","parameters":{"type":"object"}},{"name":"read","description":"read","parameters":{"type":"object"}}]}]}`),
			parse: translate.ParseGemini,
			declaration: func(out map[string]any) []any {
				return out["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := tt.parse(tt.body)
			require.NoError(t, err)
			prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			assert.Len(t, tt.declaration(out), 1)
		})
	}
}

func TestPrepareOpenAIResponses_PreservesMediumReasoningEffort(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.5", Capabilities: router.Lookup("gpt-5.5")})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, "medium", out["reasoning"].(map[string]any)["effort"])
}

func TestAdaptiveReasoningDelegatesToCrossFormatTargetDefault(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`))
	require.NoError(t, err)

	t.Run("OpenAI Responses", func(t *testing.T) {
		prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.5", Capabilities: router.Lookup("gpt-5.5")})
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(prep.Body, &out))
		assert.NotContains(t, out, "reasoning")
	})

	t.Run("Gemini", func(t *testing.T) {
		prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(prep.Body, &out))
		if config, ok := out["generationConfig"].(map[string]any); ok {
			assert.NotContains(t, config, "thinkingConfig")
		}
	})
}

func TestApplyReasoningIntent_ClampsAndRejectsUnsupportedSemantics(t *testing.T) {
	spec := router.NewSpecWithReasoning(router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}})
	clamped, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningLevel, Level: "xhigh", Explicit: true}, spec, "")
	require.NoError(t, err)
	assert.Equal(t, "high", clamped.Level)
	assert.NotEmpty(t, clamped.NormalizationNotes)

	_, err = translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningBudget, BudgetTokens: 2048, Explicit: true}, spec, "")
	require.ErrorIs(t, err, translate.ErrReasoningIncompatible)
}

func TestApplyReasoningIntent_MuseSparkAcceptsEveryLevelAndNeverDisables(t *testing.T) {
	spec := router.Lookup("muse-spark-1.3")
	for _, level := range []string{"low", "medium", "high", "xhigh"} {
		got, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningLevel, Level: level, Explicit: true}, spec, "")
		require.NoError(t, err, level)
		assert.Equal(t, level, got.Level)
		assert.Empty(t, got.NormalizationNotes, level)
	}
	got, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningDisabled, Explicit: true}, spec, "")
	require.NoError(t, err)
	assert.Equal(t, translate.ReasoningLevel, got.Kind)
	assert.Equal(t, "low", got.Level)
	assert.Equal(t, "xhigh", translate.ResolveForceEffort(spec, "ultra"))
}

func TestPrepareAnthropic_ClientCacheControlFidelity(t *testing.T) {
	t.Run("ttl is preserved and router uses remaining capacity", func(t *testing.T) {
		env, err := translate.ParseOpenAI([]byte(`{"messages":[{"role":"system","content":"rules","cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"user","content":"hi"}]}`))
		require.NoError(t, err)
		prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-4-8"})
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(prep.Body, &out))
		system := out["system"].([]any)
		assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"}, system[0].(map[string]any)["cache_control"])
		lastMessage := out["messages"].([]any)[0].(map[string]any)
		lastBlock := lastMessage["content"].([]any)[0].(map[string]any)
		assert.Equal(t, map[string]any{"type": "ephemeral"}, lastBlock["cache_control"])
	})

	t.Run("explicit overflow returns a stable validation error", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"one","cache_control":{"type":"ephemeral"}},{"role":"system","content":"two","cache_control":{"type":"ephemeral"}},{"role":"system","content":"three","cache_control":{"type":"ephemeral"}},{"role":"system","content":"four","cache_control":{"type":"ephemeral"}},{"role":"system","content":"five","cache_control":{"type":"ephemeral"}}]}`)
		env, err := translate.ParseOpenAI(body)
		require.NoError(t, err)
		_, err = env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-4-8"})
		require.ErrorIs(t, err, translate.ErrAnthropicCacheControlOverflow)
		assert.False(t, errors.Is(err, translate.ErrAnthropicCacheControlInvalid))
	})
}
