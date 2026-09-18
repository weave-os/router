package toolcheck

import (
	"errors"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// maxRepairPasses bounds the repair loop: one coercion can surface the next
// error (e.g. wrap-in-array then item type), but a payload that needs more
// than a few passes is not safely repairable.
const maxRepairPasses = 3

// repairArgs applies validation-error-driven safe coercions to args and
// returns the result plus the action names applied. Between passes it
// re-validates against schema to surface the next layer of errors; the
// caller still does the final re-validation and discards the repair when it
// fails.
//
// Safe means lossless and meaning-preserving:
//   - drop_unknown_key:        keys the schema rejects via additionalProperties
//   - coerce_string_to_number: "5" -> 5 (lossless parse only)
//   - coerce_string_to_bool:   "true" -> true
//   - coerce_to_string:        5 -> "5", true -> "true"
//   - wrap_scalar_in_array:    "x" -> ["x"]
//
// Missing required params and enum violations are NOT repairable — inventing
// values would change the call's meaning.
func repairArgs(schema *jsonschema.Schema, args string, verr error) (out string, actions []string) {
	document := newArgumentDocument(args)
	out = args
	current := verr
	for pass := 0; pass < maxRepairPasses; pass++ {
		var validationErr *jsonschema.ValidationError
		if !errors.As(current, &validationErr) {
			return out, actions
		}
		passActions := applyLeafRepairs(document, validationErr)
		if document.failed {
			return args, nil
		}
		if len(passActions) == 0 {
			return out, actions
		}
		actions = append(actions, passActions...)
		out = document.materialize()
		current = validate(schema, out)
		if current == nil {
			return out, actions
		}
	}
	return out, actions
}

// applyLeafRepairs walks every leaf validation error and mutates out in
// place. Returns the actions applied this pass.
func applyLeafRepairs(document *argumentDocument, verr *jsonschema.ValidationError) (actions []string) {
	for _, leaf := range collectLeaves(verr, nil) {
		switch k := leaf.ErrorKind.(type) {
		case *kind.AdditionalProperties:
			// The validator only emits this where the schema forbids extra
			// keys, so the additionalProperties:false gate is implicit.
			parentPath := leaf.InstanceLocation
			// The original joinPath omitted an empty encoded parent path.
			if len(parentPath) == 1 && parentPath[0] == "" {
				parentPath = nil
			}
			for _, prop := range k.Properties {
				target := make([]string, len(parentPath)+1)
				copy(target, parentPath)
				target[len(parentPath)] = prop
				if _, ok := document.delete(target); ok {
					actions = append(actions, "drop_unknown_key")
				}
			}
		case *kind.Type:
			if len(leaf.InstanceLocation) == 0 {
				continue // root-level type mismatch is not repairable
			}
			if action, ok := coerceValue(document, leaf.InstanceLocation, k); ok {
				actions = append(actions, action)
			}
		}
	}
	return actions
}

// coerceValue attempts one lossless coercion of the value at path toward the
// schema's wanted types, in fixed preference order.
func coerceValue(document *argumentDocument, path []string, k *kind.Type) (action string, ok bool) {
	value := document.lookup(path)
	if value == nil {
		return "", false
	}
	var wantNumber, wantInteger, wantBool, wantString, wantArray bool
	for _, wantedType := range k.Want {
		switch wantedType {
		case "number":
			wantNumber = true
		case "integer":
			wantInteger = true
		case "boolean":
			wantBool = true
		case "string":
			wantString = true
		case "array":
			wantArray = true
		}
	}

	if value.kind == argumentString {
		s := value.stringValue
		if wantNumber || wantInteger {
			if wantInteger {
				if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
					if document.replace(path, strconv.FormatInt(n, 10)) {
						return "coerce_string_to_number", true
					}
				}
			} else if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				if document.replace(path, strconv.FormatFloat(f, 'f', -1, 64)) {
					return "coerce_string_to_number", true
				}
			}
		}
		if wantBool {
			if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
				if document.replace(path, strconv.FormatBool(b)) {
					return "coerce_string_to_bool", true
				}
			}
		}
	}
	if wantString && (value.kind == argumentNumber || value.kind == argumentBoolean) {
		if document.replace(path, quoteArgumentString(value.raw)) {
			return "coerce_to_string", true
		}
	}
	if wantArray && value.kind != argumentArray {
		if document.wrap(path, value) {
			return "wrap_scalar_in_array", true
		}
	}
	return "", false
}

// collectLeaves flattens the error tree into its leaf causes.
func collectLeaves(verr *jsonschema.ValidationError, acc []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(verr.Causes) == 0 {
		return append(acc, verr)
	}
	for _, c := range verr.Causes {
		acc = collectLeaves(c, acc)
	}
	return acc
}
