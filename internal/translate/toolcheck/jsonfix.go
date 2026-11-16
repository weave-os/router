package toolcheck

import "strings"

func repairJSON(raw string) (out string, actions []string) {
	if len(raw) > maxArgsBytes {
		return "", nil
	}
	out = strings.TrimSpace(raw)

	if stripped, ok := stripMarkdownFence(out); ok {
		out = stripped
		actions = append(actions, "json_repair_strip_fence")
	}
	if !strings.HasPrefix(out, "{") && !strings.HasPrefix(out, "[") {
		return "", nil
	}
	if balanced, balActions := balanceJSON(out); len(balActions) > 0 {
		out = balanced
		actions = append(actions, balActions...)
	}
	if len(actions) == 0 {
		return "", nil
	}
	return out, actions
}

func stripMarkdownFence(s string) (out string, ok bool) {
	if !strings.HasPrefix(s, "```") {
		return s, false
	}
	body := strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
	return body, true
}

func balanceJSON(s string) (out string, actions []string) {
	var stack []byte
	inString := false
	escaped := false

	expectKey := false
	stringIsKey := false
	lastComplete := -1
	var b strings.Builder
	b.Grow(len(s) + 4)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
				if !stringIsKey {
					lastComplete = b.Len()
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			stringIsKey = expectKey
			b.WriteByte(c)
		case '{':
			stack = append(stack, c)
			expectKey = true
			b.WriteByte(c)
		case '[':
			stack = append(stack, c)
			expectKey = false
			b.WriteByte(c)
		case ':':
			expectKey = false
			b.WriteByte(c)
		case ',':
			expectKey = len(stack) > 0 && stack[len(stack)-1] == '{'
			b.WriteByte(c)
		case '}', ']':
			want := byte('{')
			if c == ']' {
				want = '['
			}
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return "", nil
			}
			stack = stack[:len(stack)-1]
			expectKey = false
			b.WriteByte(c)
			lastComplete = b.Len()
			if len(stack) == 0 {
				if i+1 < len(s) && strings.TrimSpace(s[i+1:]) != "" {
					actions = append(actions, "json_repair_strip_trailing")
				}
				return b.String(), actions
			}
		default:
			b.WriteByte(c)
			if c != ',' && c != ':' && !isJSONWhitespace(c) {
				lastComplete = b.Len()
			}
		}
	}

	if len(stack) == 0 && !inString {
		return "", nil
	}

	out = b.String()
	if inString && !stringIsKey {
		out += `"`
		actions = append(actions, "json_repair_close_string")
		lastComplete = len(out)
	}
	if lastComplete < 0 {
		return "", nil
	}
	if lastComplete < len(out) {
		out = out[:lastComplete]
		actions = append(actions, "json_repair_drop_partial_member")
	}
	for trimmed := strings.TrimRight(out, " \t\r\n"); strings.HasSuffix(trimmed, ","); trimmed = strings.TrimRight(out, " \t\r\n") {
		out = trimmed[:len(trimmed)-1]
		actions = append(actions, "json_repair_drop_trailing_comma")
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out += "}"
		} else {
			out += "]"
		}
	}
	actions = append(actions, "json_repair_close_brackets")
	return out, actions
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
