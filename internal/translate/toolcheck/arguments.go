package toolcheck

import (
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
	original           string
	prefix             string
	suffix             string
	root               *argumentNode
	failed             bool
	libraryEdits       bool
	rootMemberCapacity int
	firstEditApplied   bool
	// Small repairs share the root allocation instead of allocating its state separately.
	initialRoot   argumentNode
	initialSource argumentSource
	rootContainer argumentContainer
}

type argumentNode struct {
	kind        argumentNodeKind
	raw         string
	stringValue string

	changed bool
	parent  *argumentNode
	source  *argumentSource
	start   int
	*argumentContainer
}

type argumentContainer struct {
	expanded       bool
	memberIndex    map[string]argumentPositions
	firstLive      int
	lastLive       int
	trailingGap    *argumentGap
	objectMembers  []argumentMember
	objectTrailing string
	arrayElements  []argumentElement
	arrayTrailing  string
}

type argumentPositions struct {
	first int
	last  int
}

type argumentMember struct {
	raw               string
	start             int
	previousLive      int
	nextLive          int
	leadingGap        *argumentGap
	next              int
	key               string
	keyRaw            string
	memberPrefix      string
	keyValueSeparator string
	value             *argumentNode
	deleted           bool
}

type argumentElement struct {
	leadingGap    *argumentGap
	elementPrefix string
	value         *argumentNode
}

func newArgumentDocument(raw string) *argumentDocument {
	document := &argumentDocument{}
	document.reset(raw)
	return document
}

func (document *argumentDocument) reset(raw string) {
	prefixLength := len(raw) - len(strings.TrimLeftFunc(raw, isArgumentWhitespace))
	suffixLength := len(raw) - len(strings.TrimRightFunc(raw, isArgumentWhitespace))
	trimmed := raw[prefixLength : len(raw)-suffixLength]
	parsed := gjson.Parse(trimmed)
	*document = argumentDocument{
		original:      raw,
		prefix:        raw[:prefixLength],
		suffix:        raw[len(raw)-suffixLength:],
		initialRoot:   argumentNode{raw: trimmed, kind: argumentKind(parsed), stringValue: parsed.Str},
		initialSource: argumentSource{raw: trimmed},
	}
	document.root = &document.initialRoot
	document.root.source = &document.initialSource
}

// A single small edit costs less through the original path operations. A
// second edit, or a caller with a known member count, amortizes the index.
func (document *argumentDocument) firstSmallEdit() bool {
	return !document.firstEditApplied && !document.root.changed && document.root.argumentContainer == nil && document.rootMemberCapacity == 0 && len(document.original) <= 512
}

func isArgumentWhitespace(r rune) bool {
	return r <= ' '
}

func parseArgumentNode(raw string) *argumentNode {
	parsed := gjson.Parse(raw)
	node := newArgumentNode(argumentKind(parsed))
	node.raw, node.stringValue = raw, parsed.Str
	if node.kind == argumentObject || node.kind == argumentArray {
		node.source = &argumentSource{raw: raw}
	}
	return node
}

func newArgumentNode(kind argumentNodeKind) *argumentNode {
	if kind == argumentObject || kind == argumentArray {
		storage := &struct {
			node      argumentNode
			container argumentContainer
		}{}
		storage.node.kind = kind
		storage.node.argumentContainer = &storage.container
		return &storage.node
	}
	return &argumentNode{kind: kind}
}

// expandChildren leaves untouched subtrees raw. Root-only edits use GJSON's
// traversal; descending builds a shared span index so successive depths do not
// rescan the same nested suffix.
func (document *argumentDocument) expandChildren(node *argumentNode) bool {
	if node.kind != argumentObject && node.kind != argumentArray {
		return true
	}
	if node.argumentContainer != nil && node.expanded {
		return true
	}
	if node.start != 0 && node.source.spans == nil {
		node.source.expandedBytes += len(node.raw)
	}
	// Amortize the stdlib index against repeated scans, not a single sparse
	// descent. Unindexed traversal remains bounded by a fixed multiple of the source length.
	if node.start != 0 && node.source.expandedBytes > 128*len(node.source.raw) {
		err := node.source.indexContainers()
		if err != nil {
			document.failed = true
			return false
		}
	}
	if node.argumentContainer == nil {
		if node == document.root {
			node.argumentContainer = &document.rootContainer
		} else {
			node.argumentContainer = &argumentContainer{}
		}
	}
	*node.argumentContainer = argumentContainer{expanded: true, firstLive: -1, lastLive: -1}
	raw := node.raw
	cursor := 1
	if node.kind == argumentObject {
		capacity := 0
		if node == document.root {
			capacity, document.rootMemberCapacity = document.rootMemberCapacity, 0
			if capacity == 0 {
				capacity = 2
			}
		}
		node.objectMembers = make([]argumentMember, 0, capacity)
		node.firstLive, node.lastLive = -1, -1
	} else {
		node.arrayElements = make([]argumentElement, 0)
	}
	appendChild := func(key, value gjson.Result) bool {
		if node.kind == argumentObject {
			index := len(node.objectMembers)
			if node.lastLive >= 0 {
				node.objectMembers[node.lastLive].nextLive = index
			} else {
				node.firstLive = index
			}
			node.objectMembers = append(node.objectMembers, argumentMember{
				previousLive: node.lastLive, nextLive: -1,
				key: key.Str, keyRaw: key.Raw, next: -1,
				memberPrefix:      raw[cursor:key.Index],
				keyValueSeparator: raw[key.Index+len(key.Raw) : value.Index],
				raw:               value.Raw, start: node.start + value.Index,
			})
			node.lastLive = index
		} else {
			child := newArgumentNode(argumentKind(value))
			child.raw, child.stringValue = value.Raw, value.Str
			child.parent, child.source, child.start = node, node.source, node.start+value.Index
			node.arrayElements = append(node.arrayElements, argumentElement{
				elementPrefix: raw[cursor:value.Index], value: child,
			})
		}
		cursor = value.Index + len(value.Raw)
		return true
	}
	if node.source.spans == nil {
		gjson.Parse(raw).ForEach(appendChild)
	} else {
		for offset := skipArgumentSeparators(raw, 1); offset < len(raw)-1; offset = skipArgumentSeparators(raw, cursor) {
			var key gjson.Result
			if node.kind == argumentObject {
				key = gjson.Parse(raw[offset:])
				key.Index = offset
				offset = skipArgumentSeparators(raw, offset+len(key.Raw))
			}
			var value gjson.Result
			if end, ok := node.source.spans[node.start+offset]; ok {
				value = gjson.Parse(raw[offset : end-node.start])
			} else {
				value = gjson.Parse(raw[offset:])
			}
			value.Index = offset
			appendChild(key, value)
		}
	}
	if node.kind == argumentObject {
		node.objectTrailing = raw[cursor : len(raw)-1]
	} else {
		node.arrayTrailing = raw[cursor : len(raw)-1]
	}
	return true
}

func (node *argumentNode) memberNode(index int) *argumentNode {
	member := &node.objectMembers[index]
	if member.value == nil {
		parsed := gjson.Parse(member.raw)
		member.value = newArgumentNode(argumentKind(parsed))
		member.value.raw, member.value.stringValue = member.raw, parsed.Str
		member.value.parent, member.value.source, member.value.start = node, node.source, member.start
	}
	return member.value
}

func (node *argumentNode) indexMembers() {
	if node.memberIndex != nil {
		return
	}
	node.memberIndex = make(map[string]argumentPositions, len(node.objectMembers))
	for i := range node.objectMembers {
		member := &node.objectMembers[i]
		if member.deleted {
			continue
		}
		positions, exists := node.memberIndex[member.key]
		if exists {
			node.objectMembers[positions.last].next = i
			positions.last = i
		} else {
			positions = argumentPositions{first: i, last: i}
		}
		node.memberIndex[member.key] = positions
	}
}

func (node *argumentNode) firstMember(key string) int {
	if len(node.objectMembers) <= 16 {
		return node.nextMember(-1, key)
	}
	node.indexMembers()
	if positions, ok := node.memberIndex[key]; ok {
		return positions.first
	}
	return -1
}

func (node *argumentNode) nextMember(index int, key string) int {
	if node.memberIndex != nil {
		return node.objectMembers[index].next
	}
	for i := index + 1; i < len(node.objectMembers); i++ {
		if !node.objectMembers[i].deleted && node.objectMembers[i].key == key {
			return i
		}
	}
	return -1
}

func (node *argumentNode) markChanged() {
	for node != nil && !node.changed {
		node.changed = true
		node = node.parent
	}
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
	if document.failed || !document.root.changed {
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
	if !node.changed || node.argumentContainer == nil || !node.expanded {
		output.WriteString(node.raw)
		return
	}
	switch node.kind {
	case argumentObject:
		output.WriteByte('{')
		for _, member := range node.objectMembers {
			if member.deleted {
				continue
			}
			member.leadingGap.writeTo(output)
			output.WriteString(member.memberPrefix)
			output.WriteString(member.keyRaw)
			output.WriteString(member.keyValueSeparator)
			if member.value == nil {
				output.WriteString(member.raw)
			} else {
				member.value.writeTo(output)
			}
		}
		node.trailingGap.writeTo(output)
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
			element.leadingGap.writeTo(output)
			output.WriteString(prefix)
			element.value.writeTo(output)
			kept++
		}
		node.trailingGap.writeTo(output)
		output.WriteString(node.arrayTrailing)
		output.WriteByte(']')
	default:
		output.WriteString(node.raw)
	}
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
