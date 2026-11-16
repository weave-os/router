package translate

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

var ErrUntranslatableUserContent = errors.New("untranslatable user content part")

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

func anthropicBlockFromOpenAIPart(part gjson.Result) (string, error) {
	switch part.Get("type").String() {
	case "text":
		jw := newJSONWriter()
		jw.Obj()
		jw.Key("type")
		jw.Str("text")
		jw.Key("text")
		jw.Str(part.Get("text").String())
		jw.EndObj()
		return string(jw.Bytes()), nil
	case "image_url":
		if urlStr := part.Get("image_url.url").String(); urlStr != "" {
			return buildAnthropicImageBlock(urlStr), nil
		}
	case "file":
		return buildAnthropicDocumentBlock(part.Get("file"))
	case "input_audio":
		return "", fmt.Errorf("openai→anthropic: input_audio: %w", ErrUntranslatableUserContent)
	}
	return "", nil
}

func buildAnthropicDocumentBlock(file gjson.Result) (string, error) {
	mime, data, ok := parseDataURL(file.Get("file_data").String())
	if !ok || mime != "application/pdf" {
		if file.Get("file_id").String() != "" {
			return "", fmt.Errorf("openai→anthropic: file part with file_id: %w", ErrUntranslatableUserContent)
		}
		return "", fmt.Errorf("openai→anthropic: file part without inline PDF file_data: %w", ErrUntranslatableUserContent)
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("document")
	jw.Key("source")
	jw.Obj()
	jw.Key("type")
	jw.Str("base64")
	jw.Key("media_type")
	jw.Str(mime)
	jw.Key("data")
	jw.Str(data)
	jw.EndObj()
	if name := file.Get("filename").String(); name != "" {
		jw.Key("title")
		jw.Str(name)
	}
	jw.EndObj()
	return string(jw.Bytes()), nil
}

func buildAnthropicImageBlock(urlStr string) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("image")
	jw.Key("source")
	jw.Obj()
	if mime, data, ok := parseDataURL(urlStr); ok {
		jw.Key("type")
		jw.Str("base64")
		jw.Key("media_type")
		jw.Str(mime)
		jw.Key("data")
		jw.Str(data)
	} else {
		jw.Key("type")
		jw.Str("url")
		jw.Key("url")
		jw.Str(urlStr)
	}
	jw.EndObj()
	jw.EndObj()
	return string(jw.Bytes())
}

func anthropicContentHasContainerUpload(content gjson.Result) bool {
	found := false
	content.ForEach(func(_, block gjson.Result) bool {
		found = block.Get("type").String() == "container_upload"
		return !found
	})
	return found
}

func buildOpenAIFilePartFromBase64PDFDocument(block gjson.Result) string {
	if block.Get("source.type").String() != "base64" {
		return ""
	}
	mediaType := block.Get("source.media_type").String()
	data := block.Get("source.data").String()
	if mediaType != "application/pdf" || data == "" {
		return ""
	}
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("file")
	inner.Key("file")
	inner.Obj()
	inner.Key("file_data")
	inner.Str("data:" + mediaType + ";base64," + data)
	if title := block.Get("title").String(); title != "" {
		inner.Key("filename")
		inner.Str(title)
	}
	inner.EndObj()
	inner.EndObj()
	return string(inner.Bytes())
}

func buildOpenAIImagePart(block gjson.Result) string {
	src := block.Get("source")
	if !src.Exists() {
		return ""
	}
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("image_url")
	inner.Key("image_url")
	inner.Obj()
	inner.Key("url")
	switch src.Get("type").String() {
	case "base64":
		mediaType := src.Get("media_type").String()
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		data := src.Get("data").String()
		if data == "" {
			return ""
		}
		inner.Str("data:" + mediaType + ";base64," + data)
	case "url":
		urlStr := src.Get("url").String()
		if urlStr == "" {
			return ""
		}
		inner.Str(urlStr)
	default:
		return ""
	}
	inner.EndObj()
	inner.EndObj()
	return string(inner.Bytes())
}

func geminiBase64InlineDataPart(mime, data string) string {
	if _, err := base64.StdEncoding.DecodeString(data); err != nil || mime == "" || data == "" {
		return ""
	}
	return geminiInlineDataPart(mime, data)
}

func geminiInlineDataPart(mime, data string) string {
	pw := newJSONWriter()
	pw.Obj()
	pw.Key("inlineData")
	pw.Obj()
	pw.Key("mimeType")
	pw.Str(mime)
	pw.Key("data")
	pw.Str(data)
	pw.EndObj()
	pw.EndObj()
	return string(pw.Bytes())
}

func geminiPartFromAnthropicBase64PDFDocument(block gjson.Result) string {
	if block.Get("source.type").String() != "base64" {
		return ""
	}
	mime := block.Get("source.media_type").String()
	if mime != "application/pdf" {
		return ""
	}
	data := block.Get("source.data").String()
	if data == "" {
		return ""
	}
	return geminiInlineDataPart(mime, data)
}
