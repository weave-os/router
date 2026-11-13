package toolcheck

import (
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// lookup follows gjson's read behavior: when a duplicate object member has
// the requested name but lacks a deeper path, later duplicates remain eligible.
func (document *argumentDocument) lookup(path []string) *argumentNode {
	if len(path) == 0 {
		return document.root
	}
	if document.firstSmallEdit() || document.libraryEdits || argumentLibraryPath(path) {
		value := gjson.Get(document.materialize(), argumentMutationPath(path))
		if !value.Exists() {
			return nil
		}
		return parseArgumentNode(value.Raw)
	}
	return document.lookupNode(document.root, path)
}

func (document *argumentDocument) lookupNode(node *argumentNode, path []string) *argumentNode {
	if len(path) == 0 {
		return node
	}
	if !document.expandChildren(node) {
		return nil
	}
	switch node.kind {
	case argumentObject:
		for i := node.firstMember(argumentPathToken(path[0])); i >= 0; i = node.nextMember(i, argumentPathToken(path[0])) {
			if found := document.lookupNode(node.memberNode(i), path[1:]); found != nil {
				return found
			}
		}
	case argumentArray:
		index, ok := argumentArrayIndex(path[0])
		if ok && index < len(node.arrayElements) {
			return document.lookupNode(node.arrayElements[index].value, path[1:])
		}
	}
	return nil
}

func argumentArrayIndex(token string) (int, bool) {
	if token == "" {
		return 0, false
	}
	// GJSON accumulates uint64 indexes with wraparound before converting to int.
	var index uint64
	for i := 0; i < len(token); i++ {
		if token[i] < '0' || token[i] > '9' {
			return 0, false
		}
		index = index*10 + uint64(token[i]-'0')
	}
	position := int(index)
	return position, position >= 0
}

// These unescaped prefixes denote GJSON expressions, not literal member paths.
// Keep their interpretation in the existing library rather than duplicate it.
func argumentLibraryPath(path []string) bool {
	if len(path) >= 3 && path[0] == "" && path[1] == "" {
		return true
	}
	for _, token := range path {
		token = strings.TrimPrefix(token, ":")
		if strings.HasPrefix(token, "[") || strings.HasPrefix(token, "{") || strings.HasPrefix(token, "!") || strings.ContainsAny(token, "|\"") {
			return true
		}
	}
	return false
}

func argumentMutationPath(path []string) string {
	var encoded strings.Builder
	for i, token := range path {
		if i > 0 {
			encoded.WriteByte('.')
		}
		for _, r := range token {
			switch r {
			case '.', '*', '?', '\\', '|', '#', '@':
				encoded.WriteByte('\\')
			}
			encoded.WriteRune(r)
		}
	}
	return encoded.String()
}

func (document *argumentDocument) mutationParent(path []string) (*argumentNode, string) {
	current := document.root
	for _, token := range path[:len(path)-1] {
		if !document.expandChildren(current) {
			return nil, ""
		}
		switch current.kind {
		case argumentObject:
			index := current.firstMember(mutationKey(token))
			if index < 0 {
				return nil, ""
			}
			current = current.memberNode(index)
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
	if !document.expandChildren(current) {
		return nil, ""
	}
	return current, path[len(path)-1]
}

// mutationKey preserves sjson's leading-colon path semantics.
func mutationKey(token string) string {
	return strings.TrimPrefix(argumentPathToken(token), ":")
}

// The historical path encoder iterated runes, replacing invalid UTF-8 bytes.
func argumentPathToken(token string) string {
	if utf8.ValidString(token) {
		return token
	}
	return string([]rune(token))
}

func invalidArgumentPath(path []string) bool {
	return len(path) == 0 || (len(path) == 1 && path[0] == "")
}

func quoteArgumentString(raw string) string {
	encoded, _ := sjson.Set(`[]`, "0", raw) // A fixed array index cannot fail.
	return encoded[1 : len(encoded)-1]
}
