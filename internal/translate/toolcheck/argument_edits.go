package toolcheck

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (document *argumentDocument) delete(path []string) (changed, ok bool) {
	if document.firstSmallEdit() || document.libraryEdits || argumentLibraryPath(path) {
		current := document.materialize()
		raw, err := sjson.Delete(current, argumentMutationPath(path))
		if err != nil {
			return false, false
		}
		document.reset(raw)
		document.firstEditApplied = true
		document.libraryEdits = !gjson.Valid(raw)
		return current != raw, true
	}
	if invalidArgumentPath(path) {
		return false, false
	}
	parent, target := document.mutationParent(path)
	if parent == nil {
		return false, !document.failed
	}
	if parent.kind == argumentArray {
		return parent.deleteArrayElement(target)
	}
	if parent.kind != argumentObject {
		return false, true
	}
	key := mutationKey(target)
	i := parent.firstMember(key)
	if i < 0 {
		return false, true
	}
	member := &parent.objectMembers[i]
	member.deleted = true
	if member.previousLive < 0 {
		parent.firstLive = member.nextLive
		if member.nextLive >= 0 {
			next := &parent.objectMembers[member.nextLive]
			next.leadingGap = nil
			next.memberPrefix = next.memberPrefix[strings.IndexByte(next.memberPrefix, ',')+1:]
		}
	} else {
		parent.objectMembers[member.previousLive].nextLive = member.nextLive
		comma := strings.IndexByte(member.memberPrefix, ',')
		gap := member.leadingGap
		if comma > 0 {
			gap = joinArgumentGaps(gap, &argumentGap{raw: member.memberPrefix[:comma]})
		}
		if member.nextLive >= 0 {
			next := &parent.objectMembers[member.nextLive]
			next.leadingGap = joinArgumentGaps(gap, next.leadingGap)
		} else {
			parent.trailingGap = joinArgumentGaps(gap, parent.trailingGap)
		}
	}
	if member.nextLive >= 0 {
		parent.objectMembers[member.nextLive].previousLive = member.previousLive
	} else {
		parent.lastLive = member.previousLive
	}
	if parent.memberIndex != nil {
		positions := parent.memberIndex[key]
		if member.next < 0 {
			delete(parent.memberIndex, key)
		} else {
			positions.first = member.next
			parent.memberIndex[key] = positions
		}
	}
	parent.markChanged()
	return true, true
}

func (node *argumentNode) deleteArrayElement(target string) (changed, ok bool) {
	key := mutationKey(target)
	index, valid := argumentArrayIndex(key)
	if target == "-1" {
		index, valid = len(node.arrayElements)-1, true
	}
	if !valid || index < 0 || index >= len(node.arrayElements) {
		return false, true
	}
	element := node.arrayElements[index]
	if index == 0 {
		gap := element.leadingGap
		if element.elementPrefix != "" {
			gap = joinArgumentGaps(gap, &argumentGap{raw: element.elementPrefix})
		}
		if len(node.arrayElements) > 1 {
			next := &node.arrayElements[1]
			next.leadingGap = gap
			next.elementPrefix = next.elementPrefix[strings.IndexByte(next.elementPrefix, ',')+1:]
		} else {
			node.trailingGap = joinArgumentGaps(gap, node.trailingGap)
		}
	} else {
		gap := element.leadingGap
		if comma := strings.IndexByte(element.elementPrefix, ','); comma > 0 {
			gap = joinArgumentGaps(gap, &argumentGap{raw: element.elementPrefix[:comma]})
		}
		if index+1 < len(node.arrayElements) {
			next := &node.arrayElements[index+1]
			next.leadingGap = joinArgumentGaps(gap, next.leadingGap)
		} else {
			node.trailingGap = joinArgumentGaps(gap, node.trailingGap)
		}
	}
	copy(node.arrayElements[index:], node.arrayElements[index+1:])
	node.arrayElements = node.arrayElements[:len(node.arrayElements)-1]
	node.markChanged()
	return true, true
}

func (document *argumentDocument) replace(path []string, raw string) bool {
	return document.install(path, parseArgumentNode(raw))
}

func (document *argumentDocument) install(path []string, value *argumentNode) bool {
	if document.firstSmallEdit() || document.libraryEdits || argumentLibraryPath(path) {
		var replacement strings.Builder
		value.writeTo(&replacement)
		raw, err := sjson.SetRaw(document.materialize(), argumentMutationPath(path), replacement.String())
		if err != nil {
			return false
		}
		document.reset(raw)
		document.firstEditApplied = true
		document.libraryEdits = !gjson.Valid(raw)
		return true
	}
	if invalidArgumentPath(path) {
		return false
	}
	return document.installNode(document.root, path, value)
}

func (document *argumentDocument) installNode(node *argumentNode, path []string, value *argumentNode) bool {
	if len(path) == 2 && path[1] == "" {
		return node.installMissingPath(path, value)
	}
	if !document.expandChildren(node) {
		return false
	}
	key := mutationKey(path[0])
	switch node.kind {
	case argumentObject:
		index := node.firstMember(key)
		if len(path) > 1 {
			if index < 0 || (node.memberNode(index).kind != argumentObject && node.memberNode(index).kind != argumentArray) {
				ok := node.installMissingPath(path, value)
				if ok && index < 0 && node == document.root {
					document.prefix, document.suffix = "", ""
				}
				return ok
			}
			return document.installNode(node.memberNode(index), path[1:], value)
		}
		if index >= 0 {
			node.objectMembers[index].value = value
		} else {
			node.addMember(key, value)
			if node == document.root {
				document.prefix, document.suffix = "", ""
			}
		}
	case argumentArray:
		index, ok := argumentArrayIndex(key)
		if !ok || strings.HasPrefix(path[0], ":") || index >= len(node.arrayElements) {
			return node.installMissingPath(path, value)
		}
		if len(path) > 1 {
			if node.arrayElements[index].value.kind != argumentObject && node.arrayElements[index].value.kind != argumentArray {
				return node.installMissingPath(path, value)
			}
			return document.installNode(node.arrayElements[index].value, path[1:], value)
		}
		node.arrayElements[index].value = value
	default:
		return node.installMissingPath(path, value)
	}
	value.parent = node
	node.markChanged()
	return true
}

// A duplicate-parent read can find a descendant that the first write parent
// lacks. Let SJSON create that missing suffix (including its numeric/empty-key
// rules) rather than introducing a second path-creation algorithm.
func (node *argumentNode) installMissingPath(path []string, value *argumentNode) bool {
	var current, replacement strings.Builder
	node.writeTo(&current)
	value.writeTo(&replacement)
	raw, err := sjson.SetRaw(current.String(), argumentMutationPath(path), replacement.String())
	if err != nil {
		return false
	}
	parent := node.parent
	*node = *parseArgumentNode(raw)
	node.parent = parent
	node.markChanged()
	return true
}

func (node *argumentNode) addMember(key string, value *argumentNode) {
	memberPrefix := node.objectTrailing + ","
	if node.firstLive < 0 {
		memberPrefix = node.objectTrailing
	}
	node.objectTrailing = ""
	index := len(node.objectMembers)
	if node.lastLive >= 0 {
		node.objectMembers[node.lastLive].nextLive = index
	} else {
		node.firstLive = index
	}
	node.objectMembers = append(node.objectMembers, argumentMember{
		previousLive: node.lastLive, nextLive: -1, leadingGap: node.trailingGap,
		key: key, keyRaw: quoteArgumentString(key), next: -1,
		memberPrefix: memberPrefix, keyValueSeparator: ":", value: value,
	})
	node.lastLive = index
	node.trailingGap = nil
	if node.memberIndex == nil {
		return
	}
	positions, exists := node.memberIndex[key]
	if exists {
		node.objectMembers[positions.last].next = index
		positions.last = index
	} else {
		positions = argumentPositions{first: index, last: index}
	}
	node.memberIndex[key] = positions
}

// wrap preserves current child edits. A read through a later duplicate parent
// must instead copy the subtree: SJSON historically wrote an independent value
// at the first parent, leaving the later one eligible for subsequent repairs.
func (document *argumentDocument) wrap(path []string, value *argumentNode) bool {
	if document.firstSmallEdit() || document.libraryEdits || argumentLibraryPath(path) {
		return document.install(path, newArgumentArray(value))
	}
	if invalidArgumentPath(path) {
		return false
	}
	parent, target := document.mutationParent(path)
	var current *argumentNode
	if parent != nil {
		switch parent.kind {
		case argumentObject:
			if index := parent.firstMember(mutationKey(target)); index >= 0 {
				current = parent.memberNode(index)
			}
		case argumentArray:
			if index, ok := argumentArrayIndex(mutationKey(target)); ok && index < len(parent.arrayElements) {
				current = parent.arrayElements[index].value
			}
		}
	}
	if current != value {
		value = value.snapshot()
	}
	wrapper := newArgumentArray(value)
	if !document.install(path, wrapper) {
		return false
	}
	value.parent = wrapper
	return true
}

func newArgumentArray(value *argumentNode) *argumentNode {
	array := newArgumentNode(argumentArray)
	array.changed, array.expanded = true, true
	array.arrayElements = []argumentElement{{value: value}}
	return array
}

func (node *argumentNode) snapshot() *argumentNode {
	copy := newArgumentNode(node.kind)
	container := copy.argumentContainer
	*copy = *node
	copy.parent = nil
	copy.argumentContainer = container
	if node.argumentContainer == nil || !node.expanded {
		return copy
	}
	*container = *node.argumentContainer
	copy.memberIndex = nil
	if node.objectMembers != nil {
		copy.objectMembers = append([]argumentMember{}, node.objectMembers...)
		for i := range copy.objectMembers {
			member := &copy.objectMembers[i]
			if member.value != nil {
				member.value = member.value.snapshot()
				member.value.parent = copy
			}
			member.next = -1
		}
	}
	if node.arrayElements != nil {
		copy.arrayElements = append([]argumentElement{}, node.arrayElements...)
		for i := range copy.arrayElements {
			element := &copy.arrayElements[i]
			element.value = element.value.snapshot()
			element.value.parent = copy
		}
	}
	return copy
}

func removeFirstArgumentMember(raw string, objectStart, valueEnd int) string {
	suffixStart := valueEnd
	for next := valueEnd; next < len(raw); next++ {
		if raw[next] <= ' ' {
			continue
		}
		if raw[next] == ',' {
			suffixStart = next + 1
		}
		break
	}
	var output strings.Builder
	output.Grow(objectStart + 1 + len(raw) - suffixStart)
	output.WriteString(raw[:objectStart+1])
	output.WriteString(raw[suffixStart:])
	return output.String()
}
