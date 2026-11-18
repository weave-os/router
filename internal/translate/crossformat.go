package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

func parseDataURL(url string) (mime, data string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(url, "data:")
	mime, data, ok = strings.Cut(rest, ";base64,")
	if !ok || data == "" {
		return "", "", false
	}
	return mime, data, true
}

func extractThoughtSignature(part gjson.Result) string {
	if sig := part.Get("thought_signature").String(); sig != "" {
		return sig
	}
	if sig := part.Get("function.thought_signature").String(); sig != "" {
		return sig
	}
	if id := part.Get("id").String(); id != "" {
		if _, sig := extractSignatureFromID(id); sig != "" {
			return sig
		}
	}
	return ""
}

func toolResultContentGJSON(content gjson.Result) string {
	switch content.Type {
	case gjson.String:
		return content.String()
	case gjson.JSON:
		if !content.IsArray() {
			return ""
		}
		var parts []string
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if text := block.Get("text").String(); text != "" {
					parts = append(parts, text)
				}
			}
			return true
		})
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
