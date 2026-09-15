package toolcheck

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

type argumentNodeKind uint8

const (
	argumentObject argumentNodeKind = iota
	argumentArray
	argumentString
	argumentNumber
	argumentBoolean
	argumentNull
)

// argumentDocument keeps JSON object members in wire order. gjson's traversal
// supplies both decoded keys and raw value spans, so edits can retain duplicate
// members, number lexemes, and untouched subtrees without reparsing the whole
// argument string for every repair operation.
type argumentDocument struct {
	original string
	prefix   string
	suffix   string
	root     *argumentNode
	changed  bool
}

type argumentNode struct {
	kind        argumentNodeKind
	raw         string
	stringValue string

	changed          bool
	objectMembers    []argumentMember
	objectTrailing   string
	arrayElements    []argumentElement
	arrayEmptyPrefix string
	arrayTrailing    string
}

type argumentMember struct {
	key               string
	keyRaw            string
	memberPrefix      string
	keyValueSeparator string
	value             *argumentNode
	deleted           bool
}

type argumentElement struct {
	elementPrefix string
	value         *argumentNode
}

func newArgumentDocument(raw string) *argumentDocument {
	trimmed := strings.TrimSpace(raw)
	prefixLength := len(raw) - len(strings.TrimLeftFunc(raw, func(r rune) bool { return r <= ' ' }))
	suffixLength := len(raw) - len(strings.TrimRightFunc(raw, func(r rune) bool { return r <= ' ' }))
	rootRaw := trimmed
	if prefixLength > 0 {
		rootRaw = raw[prefixLength:]
	}
	if suffixLength > 0 {
		rootRaw = rootRaw[:len(rootRaw)-suffixLength]
	}
	return &argumentDocument{
		original: raw,
		prefix:   raw[:prefixLength],
		suffix:   raw[len(raw)-suffixLength:],
		root:     parseArgumentNode(rootRaw),
	}
}

func parseArgumentNode(raw string) *argumentNode {
	parsed := gjson.Parse(raw)
	node := &argumentNode{raw: raw, kind: argumentKind(parsed), stringValue: parsed.Str}
	if parsed.IsObject() {
		node.objectMembers = make([]argumentMember, 0)
		cursor := 1
		parsed.ForEach(func(key, value gjson.Result) bool {
			keyStart := key.Index
			valueStart := value.Index
			keyEnd := keyStart + len(key.Raw)
			node.objectMembers = append(node.objectMembers, argumentMember{
				key:               key.Str,
				keyRaw:            key.Raw,
				memberPrefix:      raw[cursor:keyStart],
				keyValueSeparator: raw[keyEnd:valueStart],
				value:             parseArgumentNode(value.Raw),
			})
			cursor = valueStart + len(value.Raw)
			return true
		})
		node.objectTrailing = raw[cursor : len(raw)-1]
		return node
	}
	if parsed.IsArray() {
		node.arrayElements = make([]argumentElement, 0)
		cursor := 1
		parsed.ForEach(func(_, value gjson.Result) bool {
			valueStart := value.Index
			node.arrayElements = append(node.arrayElements, argumentElement{
				elementPrefix: raw[cursor:valueStart],
				value:         parseArgumentNode(value.Raw),
			})
			cursor = valueStart + len(value.Raw)
			return true
		})
		node.arrayTrailing = raw[cursor : len(raw)-1]
	}
	return node
}

func argumentKind(parsed gjson.Result) argumentNodeKind {
	switch parsed.Type {
	case gjson.JSON:
		if parsed.IsArray() {
			return argumentArray
		}
		return argumentObject
	case gjson.String:
		return argumentString
	case gjson.Number:
		return argumentNumber
	case gjson.True, gjson.False:
		return argumentBoolean
	default:
		return argumentNull
	}
}

func (document *argumentDocument) materialize() string {
	if !document.changed {
		return document.original
	}
	var output strings.Builder
	output.Grow(len(document.original))
	output.WriteString(document.prefix)
	document.root.writeTo(&output)
	output.WriteString(document.suffix)
	return output.String()
}

func (node *argumentNode) writeTo(output *strings.Builder) {
	if !node.hasNestedChanges() {
		output.WriteString(node.raw)
		return
	}
	switch node.kind {
	case argumentObject:
		kept := 0
		output.WriteByte('{')
		for _, member := range node.objectMembers {
			if member.deleted {
				continue
			}
			prefix := member.memberPrefix
			if kept == 0 {
				prefix = removeLeadingComma(prefix)
			}
			output.WriteString(prefix)
			output.WriteString(member.keyRaw)
			output.WriteString(member.keyValueSeparator)
			member.value.writeTo(output)
			kept++
		}
		if kept == 0 {
			if len(node.objectMembers) > 0 {
				output.WriteString(node.objectMembers[0].memberPrefix)
			}
			output.WriteString(node.objectTrailing)
			output.WriteString("}")
			return
		}
		output.WriteString(node.objectTrailing)
		output.WriteByte('}')
	case argumentArray:
		output.WriteByte('[')
		kept := 0
		for _, element := range node.arrayElements {
			prefix := element.elementPrefix
			if kept == 0 {
				prefix = removeLeadingComma(prefix)
			}
			output.WriteString(prefix)
			element.value.writeTo(output)
			kept++
		}
		if kept == 0 {
			output.WriteString(node.arrayEmptyPrefix)
		}
		output.WriteString(node.arrayTrailing)
		output.WriteByte(']')
	default:
		output.WriteString(node.raw)
	}
}

func (node *argumentNode) hasNestedChanges() bool {
	if node.changed {
		return true
	}
	switch node.kind {
	case argumentObject:
		for _, member := range node.objectMembers {
			if member.deleted || member.value.hasNestedChanges() {
				return true
			}
		}
	case argumentArray:
		for _, element := range node.arrayElements {
			if element.value.hasNestedChanges() {
				return true
			}
		}
	}
	return false
}

func removeLeadingComma(prefix string) string {
	for i := 0; i < len(prefix); i++ {
		if prefix[i] <= ' ' {
			continue
		}
		if prefix[i] == ',' {
			return prefix[:i] + prefix[i+1:]
		}
		break
	}
	return prefix
}

// lookup follows gjson's read behavior: when a duplicate object member has
// the requested name but lacks a deeper path, later duplicates remain eligible.
func (document *argumentDocument) lookup(path []string) *argumentNode {
	if len(path) == 0 {
		return document.root
	}
	return lookupArgumentNode(document.root, path)
}

func lookupArgumentNode(node *argumentNode, path []string) *argumentNode {
	if len(path) == 0 {
		return node
	}
	switch node.kind {
	case argumentObject:
		for _, member := range node.objectMembers {
			if member.deleted || member.key != path[0] {
				continue
			}
			if found := lookupArgumentNode(member.value, path[1:]); found != nil {
				return found
			}
		}
	case argumentArray:
		index, ok := argumentArrayIndex(path[0])
		if ok && index < len(node.arrayElements) {
			return lookupArgumentNode(node.arrayElements[index].value, path[1:])
		}
	}
	return nil
}

func argumentArrayIndex(token string) (int, bool) {
	if token == "" {
		return 0, false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < '0' || token[i] > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(token)
	return index, err == nil
}

func (document *argumentDocument) delete(path []string) (changed, ok bool) {
	if invalidArgumentPath(path) {
		return false, false
	}
	parent, target := document.mutationParent(path)
	if parent == nil {
		return false, true
	}
	if parent.kind == argumentObject {
		key := mutationKey(target)
		for i := range parent.objectMembers {
			if !parent.objectMembers[i].deleted && parent.objectMembers[i].key == key {
				parent.objectMembers[i].deleted = true
				document.changed = true
				return true, true
			}
		}
		return false, true
	}
	if parent.kind == argumentArray {
		targetKey := mutationKey(target)
		arrayIndex := -1
		if targetKey == "-1" {
			arrayIndex = len(parent.arrayElements) - 1
		} else {
			arrayIndex, _ = argumentArrayIndex(targetKey)
		}
		if arrayIndex >= 0 && arrayIndex < len(parent.arrayElements) {
			removedPrefix := parent.arrayElements[arrayIndex].elementPrefix
			parent.arrayElements = append(parent.arrayElements[:arrayIndex], parent.arrayElements[arrayIndex+1:]...)
			if len(parent.arrayElements) == 0 {
				parent.arrayEmptyPrefix = removeLeadingComma(removedPrefix)
			}
			parent.changed = true
			document.changed = true
			return true, true
		}
	}
	return false, true
}

func (document *argumentDocument) replace(path []string, raw string) bool {
	if invalidArgumentPath(path) {
		return false
	}
	parent, target := document.mutationParent(path)
	if parent == nil {
		return false
	}
	if parent.kind == argumentObject {
		key := mutationKey(target)
		for i := range parent.objectMembers {
			if !parent.objectMembers[i].deleted && parent.objectMembers[i].key == key {
				parent.objectMembers[i].value.replace(raw)
				document.changed = true
				return true
			}
		}
		parent.addMember(key, raw)
		document.changed = true
		return true
	}
	if parent.kind == argumentArray {
		targetKey := mutationKey(target)
		if targetKey == "-1" {
			memberPrefix := parent.arrayTrailing
			if len(parent.arrayElements) > 0 {
				memberPrefix += ","
			}
			parent.arrayElements = append(parent.arrayElements, argumentElement{
				elementPrefix: memberPrefix,
				value:         parseArgumentNode(raw),
			})
			parent.arrayTrailing = ""
			parent.changed = true
			document.changed = true
			return true
		}
		if index, ok := argumentArrayIndex(targetKey); ok && index < len(parent.arrayElements) {
			parent.arrayElements[index].value.replace(raw)
			document.changed = true
			return true
		}
	}
	return false
}

func (node *argumentNode) addMember(key, raw string) {
	memberPrefix := ","
	if len(node.objectMembers) == 0 {
		memberPrefix = node.objectTrailing
		node.objectTrailing = ""
	}
	node.objectMembers = append(node.objectMembers, argumentMember{
		key:               key,
		keyRaw:            quoteArgumentString(key),
		memberPrefix:      memberPrefix,
		keyValueSeparator: ":",
		value:             parseArgumentNode(raw),
	})
	node.changed = true
}

func (document *argumentDocument) mutationParent(path []string) (*argumentNode, string) {
	current := document.root
	for _, token := range path[:len(path)-1] {
		switch current.kind {
		case argumentObject:
			key := mutationKey(token)
			var next *argumentNode
			for _, member := range current.objectMembers {
				if !member.deleted && member.key == key {
					next = member.value
					break
				}
			}
			if next == nil {
				return nil, ""
			}
			current = next
		case argumentArray:
			index, ok := argumentArrayIndex(mutationKey(token))
			if !ok || index >= len(current.arrayElements) {
				return nil, ""
			}
			current = current.arrayElements[index].value
		default:
			return nil, ""
		}
	}
	return current, path[len(path)-1]
}

func mutationKey(token string) string {
	return strings.TrimPrefix(token, ":")
}

func invalidArgumentPath(path []string) bool {
	return len(path) == 0 || (len(path) == 1 && path[0] == "")
}

func (node *argumentNode) replace(raw string) {
	replacement := parseArgumentNode(raw)
	node.kind = replacement.kind
	node.raw = raw
	node.stringValue = replacement.stringValue
	node.objectMembers = replacement.objectMembers
	node.objectTrailing = replacement.objectTrailing
	node.arrayElements = replacement.arrayElements
	node.arrayEmptyPrefix = replacement.arrayEmptyPrefix
	node.arrayTrailing = replacement.arrayTrailing
	node.changed = true
}

func quoteArgumentString(raw string) string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return `"` + raw + `"`
	}
	return string(encoded)
}
