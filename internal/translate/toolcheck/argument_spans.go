package toolcheck

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// argumentSource indexes only container boundaries. Scalars and keys still use
// GJSON's decoding, and nodes retain slices of this immutable source.
type argumentSource struct {
	raw           string
	spans         map[int]int
	err           error
	expandedBytes int
}

type argumentContainerFrame struct {
	start     int
	delimiter json.Delim
	key       bool
}

func (source *argumentSource) indexContainers() error {
	if source.spans != nil || source.err != nil {
		return source.err
	}
	decoder := json.NewDecoder(strings.NewReader(source.raw))
	decoder.UseNumber()
	spans := make(map[int]int)
	var stack []argumentContainerFrame
	var scalar json.RawMessage
	for {
		offset := skipArgumentSeparators(source.raw, int(decoder.InputOffset()))
		key := len(stack) > 0 && stack[len(stack)-1].key
		if offset < len(source.raw) && !key && !strings.ContainsRune("{}[]", rune(source.raw[offset])) {
			err := decoder.Decode(&scalar)
			if err != nil {
				source.err = err
				return err
			}
			if len(stack) > 0 && stack[len(stack)-1].delimiter == '{' {
				stack[len(stack)-1].key = true
			}
			continue
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) && len(stack) == 0 {
			source.spans = spans
			return nil
		}
		if err != nil {
			source.err = err
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			stack[len(stack)-1].key = false
			continue
		}
		offset = int(decoder.InputOffset())
		switch delimiter {
		case '{', '[':
			if len(stack) > 0 && stack[len(stack)-1].delimiter == '{' {
				stack[len(stack)-1].key = true
			}
			stack = append(stack, argumentContainerFrame{start: offset - 1, delimiter: delimiter, key: delimiter == '{'})
		case '}', ']':
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			spans[start.start] = offset
		}
	}
}

// skipArgumentSeparators only advances within a previously validated container;
// it does not decide whether a JSON document is valid.
func skipArgumentSeparators(raw string, offset int) int {
	for offset < len(raw) {
		if raw[offset] > ' ' && raw[offset] != ',' && raw[offset] != ':' {
			break
		}
		offset++
	}
	return offset
}
