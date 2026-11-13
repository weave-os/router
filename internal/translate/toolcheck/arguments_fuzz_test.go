package toolcheck

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type argumentEditKind byte

const (
	argumentEditReplace argumentEditKind = iota
	argumentEditDelete
	argumentEditWrap
)

func FuzzArgumentDocumentEdits(f *testing.F) {
	for _, raw := range []string{
		`{"n":"1","x":null,"x":"keep","x":"","a":{"y":1},"a":{"x":{"n":"2"}}}`,
		" \n{ \"a.b\" : {\"*\":\"\\u0061\",\"?\":2},\"arr\":[1e+06, true, {\"n\":\"3\"}],\":n\":1,\"n\":2,\"\":null }\t",
		`{"x":{"n":"1","extra":true}}`, `{"x":{},"x":{"n":"2"}}`, `{"0":"value","\\":"x","@":{},"#":true,"|":null}`,
	} {
		f.Add(raw, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	}
	f.Fuzz(func(t *testing.T, raw string, operations []byte) {
		if len(raw) > 16384 || len(operations) > 64 || !json.Valid([]byte(raw)) {
			return
		}
		document := newArgumentDocument(raw)
		oracle := raw
		for step, operation := range operations {
			if len(oracle) > 65536 {
				return
			}
			paths := argumentFuzzPaths(gjson.Parse(oracle), nil, nil)
			if len(paths) == 0 {
				return
			}
			path := paths[int(operation)%len(paths)]
			encodedPath := argumentOraclePath(path)
			read := gjson.Get(oracle, encodedPath)
			node := document.lookup(path)
			if read.Exists() != (node != nil) {
				t.Fatalf("step %d path %q: read presence differs in %s", step, path, oracle)
			}
			if node == nil {
				continue
			}
			var current strings.Builder
			node.writeTo(&current)
			if current.String() != read.Raw {
				t.Fatalf("step %d path %q: read=%s want=%s", step, path, current.String(), read.Raw)
			}
			// SJSON pads absent array indexes. Bound generated requests so the
			// oracle cannot allocate billions of nulls; production has no cap.
			oversizedIndex := false
			for _, token := range path {
				token = strings.TrimPrefix(token, ":")
				if token == "" || strings.Trim(token, "0123456789") != "" {
					continue
				}
				index, err := strconv.ParseUint(token, 10, 64)
				if err != nil || index > 4096 {
					oversizedIndex = true
				}
			}
			if oversizedIndex {
				continue
			}
			var next string
			var err error
			var ok bool
			switch argumentEditKind(operation % 3) {
			case argumentEditReplace:
				replacement := []string{`0`, `true`, `"a\n\u0000\\"`, `{"n":1}`, `[1,"2"]`}[step%5]
				next, err = sjson.SetRaw(oracle, encodedPath, replacement)
				ok = document.replace(path, replacement)
			case argumentEditDelete:
				parent := gjson.Parse(oracle)
				if len(path) > 1 {
					parent = gjson.Get(oracle, argumentOraclePath(path[:len(path)-1]))
				}
				if !parent.IsObject() {
					continue // AdditionalProperties repairs delete object members only.
				}
				next, err = sjson.Delete(oracle, encodedPath)
				_, ok = document.delete(path)
			case argumentEditWrap:
				if read.IsArray() || invalidArgumentPath(path) {
					continue
				}
				next, err = sjson.SetRaw(oracle, encodedPath, "["+read.Raw+"]")
				ok = document.wrap(path, node)
			}
			if ok != (err == nil) {
				t.Fatalf("step %d op %d path %q: mutation ok=%t, oracle err=%v; %s", step, operation%3, path, ok, err, oracle)
			}
			if err == nil {
				oracle = next
			}
			if got := document.materialize(); got != oracle {
				t.Fatalf("step %d op %d path %q: got=%s want=%s", step, operation%3, path, got, oracle)
			}
		}
	})
}

func argumentFuzzPaths(value gjson.Result, prefix []string, paths [][]string) [][]string {
	if len(prefix) >= 8 {
		return paths
	}
	value.ForEach(func(key, child gjson.Result) bool {
		var token string
		if value.IsObject() {
			token = key.Str
		} else if value.IsArray() {
			token = strconv.FormatInt(key.Int(), 10)
		} else {
			return false
		}
		path := append(append([]string(nil), prefix...), token)
		paths = append(paths, path)
		if child.IsObject() || child.IsArray() {
			paths = argumentFuzzPaths(child, path, paths)
		}
		return len(paths) < 512
	})
	return paths
}

func argumentOraclePath(path []string) string {
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
