package translate

import (
	"reflect"
	"testing"
)

func TestInlineSchemaDefs_resolvesDefsRef(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"attachment": map[string]any{"$ref": "#/$defs/Attachment"},
		},
		"$defs": map[string]any{
			"Attachment": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string"},
				},
			},
		},
	}

	out := mustAs[map[string]any](t, inlineSchemaDefs(in))

	if _, ok := out["$defs"]; ok {
		t.Fatalf("$defs not stripped: %#v", out)
	}
	got := mustAs[map[string]any](t, out["properties"])["attachment"]
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ref not inlined.\n got=%#v\nwant=%#v", got, want)
	}
}

func TestInlineSchemaDefs_resolvesDefinitionsRef(t *testing.T) {
	in := map[string]any{
		"properties": map[string]any{
			"x": map[string]any{"$ref": "#/definitions/Foo"},
		},
		"definitions": map[string]any{
			"Foo": map[string]any{"type": "string"},
		},
	}
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	if _, ok := out["definitions"]; ok {
		t.Fatalf("definitions not stripped")
	}
	got := mustAs[map[string]any](t, out["properties"])["x"]
	want := map[string]any{"type": "string"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestInlineSchemaDefs_resolvesNestedRef(t *testing.T) {
	in := map[string]any{
		"properties": map[string]any{
			"outer": map[string]any{"$ref": "#/$defs/Outer"},
		},
		"$defs": map[string]any{
			"Outer": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"inner": map[string]any{"$ref": "#/$defs/Inner"},
				},
			},
			"Inner": map[string]any{"type": "integer"},
		},
	}
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	outer := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["outer"])
	inner := mustAs[map[string]any](t, outer["properties"])["inner"]
	want := map[string]any{"type": "integer"}
	if !reflect.DeepEqual(inner, want) {
		t.Fatalf("nested ref not inlined: %#v", inner)
	}
}

func TestInlineSchemaDefs_handlesCycle(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"root": map[string]any{"$ref": "#/$defs/Node"},
		},
		"$defs": map[string]any{
			"Node": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"next": map[string]any{"$ref": "#/$defs/Node"},
				},
			},
		},
	}
	// Must not infinite-loop. The inner $ref is left intact when cycle detected.
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	root := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["root"])
	next := mustAs[map[string]any](t, mustAs[map[string]any](t, root["properties"])["next"])
	if next["$ref"] != "#/$defs/Node" {
		t.Fatalf("expected cyclic $ref preserved, got %#v", next)
	}
}

func TestInlineSchemaDefs_unresolvedRefPreserved(t *testing.T) {
	in := map[string]any{
		"properties": map[string]any{
			"x": map[string]any{"$ref": "#/$defs/Missing"},
		},
		"$defs": map[string]any{
			"Other": map[string]any{"type": "string"},
		},
	}
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	got := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["x"])
	if got["$ref"] != "#/$defs/Missing" {
		t.Fatalf("unresolved $ref dropped: %#v", got)
	}
}

func TestInlineSchemaDefs_noDefsIsNoop(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
		},
	}
	out := inlineSchemaDefs(in)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("no-defs case mutated input: %#v", out)
	}
}

func TestInlineSchemaDefs_resolvesRefWithSiblingKeys(t *testing.T) {
	// Pydantic v2 / OpenAPI 3.1 (and MCP servers built on them, e.g. Intuit
	// QuickBooks) emit a $ref alongside annotation siblings like description
	// or title. Per JSON Schema Draft 7 siblings to $ref are ignored, so the
	// $ref must still be resolved — otherwise $defs gets stripped and the
	// upstream sees an unresolvable reference (Fireworks 400s with
	// "Error resolving schema reference '#/$defs/X'").
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"payment_link_type": map[string]any{
				"$ref":        "#/$defs/PaymentLinkType",
				"description": "Type of payment link",
			},
		},
		"$defs": map[string]any{
			"PaymentLinkType": map[string]any{
				"enum": []any{"link", "ach"},
				"type": "string",
			},
		},
	}
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	if _, ok := out["$defs"]; ok {
		t.Fatalf("$defs not stripped: %#v", out)
	}
	got := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["payment_link_type"])
	if _, hasRef := got["$ref"]; hasRef {
		t.Fatalf("unresolved $ref remains alongside siblings: %#v", got)
	}
	if got["type"] != "string" {
		t.Fatalf("target type not inlined: %#v", got)
	}
}

func TestInlineSchemaDefs_inlineCopiesNotShares(t *testing.T) {
	// Two refs to the same def should produce independent copies, so a later
	// in-place mutation (e.g. sanitizeOpenAIToolSchema) can't bleed across.
	in := map[string]any{
		"properties": map[string]any{
			"a": map[string]any{"$ref": "#/$defs/T"},
			"b": map[string]any{"$ref": "#/$defs/T"},
		},
		"$defs": map[string]any{
			"T": map[string]any{"type": "array"},
		},
	}
	out := mustAs[map[string]any](t, inlineSchemaDefs(in))
	a := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["a"])
	b := mustAs[map[string]any](t, mustAs[map[string]any](t, out["properties"])["b"])
	a["mutated"] = true
	if _, leaked := b["mutated"]; leaked {
		t.Fatalf("inlined refs share underlying map: %#v", b)
	}
}
