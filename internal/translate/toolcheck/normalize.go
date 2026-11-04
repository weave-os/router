package toolcheck

import (
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Bound recursive references and union exploration on untrusted schemas.
const maxNormalizationDepth = 64
const maxNormalizationBranches = 64
const maxNormalizationNodes = 10000

func normalizationRootSchema(schema *jsonschema.Schema) *jsonschema.Schema {
	for depth := 0; schema != nil && depth < maxNormalizationDepth; depth++ {
		if schema.Ref == nil {
			return schema
		}
		if schema.DraftVersion >= 2019 {
			return nil // newer drafts also enforce $ref siblings
		}
		schema = schema.Ref
	}
	return nil
}

func canNormalizeRootOptionals(schema *jsonschema.Schema) bool {
	if schema == nil {
		return true // retain empty-string compatibility for uncompilable schemas
	}
	root := normalizationRootSchema(schema)
	return root != nil && !hasNormalizationConstraints(root) && len(root.AnyOf) == 0
}

// These constraints can couple a member's presence to other values. Leaving
// the subtree intact is safer than inferring optionality from properties alone.
func hasNormalizationConstraints(schema *jsonschema.Schema) bool {
	return schema.Ref != nil || schema.RecursiveRef != nil || schema.DynamicRef != nil ||
		len(schema.AllOf) > 0 || len(schema.OneOf) > 0 || schema.Not != nil || schema.If != nil ||
		len(schema.Dependencies) > 0 || len(schema.DependentRequired) > 0 || len(schema.DependentSchemas) > 0 ||
		schema.UnevaluatedProperties != nil || schema.UnevaluatedItems != nil ||
		schema.MinProperties != nil || schema.Contains != nil || schema.UniqueItems ||
		schema.Enum != nil || schema.Const != nil
}

func normalizeNullOptionals(args string, schema *jsonschema.Schema) (string, []string) {
	if schema == nil || len(args) > maxArgsBytes || !strings.Contains(args, "null") {
		return args, nil
	}
	document := newArgumentDocument(args)
	remaining := maxNormalizationNodes
	actions := normalizeNullNode(document, document.root, schema, 0, &remaining)
	if document.failed || remaining <= 0 {
		return args, nil
	}
	return document.materialize(), actions
}

func normalizeNullNode(document *argumentDocument, node *argumentNode, schema *jsonschema.Schema, depth int, remaining *int) []string {
	if *remaining <= 0 || schema == nil || depth >= maxNormalizationDepth || (node.kind != argumentObject && node.kind != argumentArray) {
		return nil
	}
	*remaining--
	if schema.Ref != nil {
		if schema.DraftVersion < 2019 {
			return normalizeNullNode(document, node, schema.Ref, depth+1, remaining)
		}
		return normalizeNullBranches(node, schema, []*jsonschema.Schema{schema.Ref}, depth, remaining)
	}
	if hasNormalizationConstraints(schema) || len(schema.PatternProperties) > 0 {
		return nil
	}
	if len(schema.AnyOf) > 0 {
		// A sibling object/array schema can impose different required fields.
		if len(schema.Properties) > 0 || len(schema.Required) > 0 || schema.Items != nil || schema.Items2020 != nil || len(schema.PrefixItems) > 0 {
			return nil
		}
		return normalizeNullBranches(node, schema, schema.AnyOf, depth, remaining)
	}
	if !document.expandChildren(node) {
		return nil
	}
	var actions []string
	if node.kind == argumentObject {
		node.indexMembers()
		if len(node.memberIndex) != len(node.objectMembers) {
			return nil // duplicate keys have no unambiguous schema interpretation
		}
		for i, member := range node.objectMembers {
			property := schema.Properties[member.key]
			if property == nil {
				continue
			}
			child := node.memberNode(i)
			if child.kind == argumentNull && !slices.Contains(schema.Required, member.key) && validate(property, "null") != nil {
				node.deleteMember(i)
				actions = append(actions, "drop_null_optional")
				continue
			}
			actions = append(actions, normalizeNullNode(document, child, property, depth+1, remaining)...)
		}
	} else {
		for i, element := range node.arrayElements {
			actions = append(actions, normalizeNullNode(document, element.value, normalizationItemSchema(schema, i), depth+1, remaining)...)
		}
	}
	return actions
}

// Only accept a union repair when every successful branch produces identical
// arguments. Already-valid branches win, preserving legitimate nullable values.
func normalizeNullBranches(node *argumentNode, schema *jsonschema.Schema, branches []*jsonschema.Schema, depth int, remaining *int) []string {
	if len(branches) > maxNormalizationBranches || validate(schema, node.raw) == nil {
		return nil
	}
	var acceptedArguments string
	var actions []string
	for _, branch := range branches {
		if *remaining <= 0 {
			return nil
		}
		candidate := newArgumentDocument(node.raw)
		candidateActions := normalizeNullNode(candidate, candidate.root, branch, depth+1, remaining)
		if candidate.failed || len(candidateActions) == 0 {
			continue
		}
		normalized := candidate.materialize()
		if validate(branch, normalized) != nil || validate(schema, normalized) != nil {
			continue
		}
		if acceptedArguments != "" && acceptedArguments != normalized {
			return nil
		}
		acceptedArguments, actions = normalized, candidateActions
	}
	if acceptedArguments != "" {
		parent := node.parent
		*node = *parseArgumentNode(acceptedArguments)
		node.parent = parent
		node.markChanged()
	}
	return actions
}

func normalizationItemSchema(schema *jsonschema.Schema, index int) *jsonschema.Schema {
	if index < len(schema.PrefixItems) {
		return schema.PrefixItems[index]
	}
	if schema.Items2020 != nil {
		return schema.Items2020
	}
	switch items := schema.Items.(type) {
	case *jsonschema.Schema:
		return items
	case []*jsonschema.Schema:
		if index < len(items) {
			return items[index]
		}
		additional, _ := schema.AdditionalItems.(*jsonschema.Schema)
		return additional
	}
	return nil
}
