package translate

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

var ErrUnsupportedUserPart = errors.New("unsupported user content part")

type UnsupportedUserPartError struct {
	SourceFormat Format
	TargetFamily string
	PartType     string
	Reason       string
}

func (e *UnsupportedUserPartError) Error() string {
	msg := fmt.Sprintf("unsupported user content part %q for %s target", e.PartType, e.TargetFamily)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *UnsupportedUserPartError) Unwrap() error {
	return ErrUnsupportedUserPart
}

func newUnsupportedUserPart(src Format, target, partType, reason string) error {
	return &UnsupportedUserPartError{
		SourceFormat: src,
		TargetFamily: target,
		PartType:     partType,
		Reason:       reason,
	}
}

type userPartCollector struct {
	parts       []string
	skippedType string
	err         error
}

func (c *userPartCollector) add(part string, err error) bool {
	if err != nil {
		return c.fail(err)
	}
	c.parts = append(c.parts, part)
	return true
}

func (c *userPartCollector) fail(err error) bool {
	c.err = err
	return false
}

func (c *userPartCollector) skip(partType string) bool {
	if c.skippedType == "" {
		c.skippedType = reportableUserPartType(partType)
	}
	return true
}

var reportableUserPartTypes = map[string]bool{
	"text":             true,
	"image":            true,
	"image_url":        true,
	"file":             true,
	"input_file":       true,
	"input_audio":      true,
	"document":         true,
	"container_upload": true,
}

func reportableUserPartType(partType string) string {
	if reportableUserPartTypes[partType] {
		return partType
	}
	return "unknown"
}

func (c *userPartCollector) finish(src Format, target, reason string) ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.skippedType != "" {
		return nil, newUnsupportedUserPart(src, target, c.skippedType, reason)
	}
	return c.parts, nil
}

func isValidBase64(data string) bool {
	_, err := base64.StdEncoding.DecodeString(data)
	return err == nil
}

func buildAnthropicUserMessage(role string, content gjson.Result) (string, error) {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("role")
	jw.Str(role)
	jw.Key("content")
	if err := writeAnthropicUserContentValue(jw, content); err != nil {
		return "", err
	}
	jw.EndObj()
	return string(jw.Bytes()), nil
}

func writeAnthropicUserContentValue(jw *jsonWriter, content gjson.Result) error {
	if !content.IsArray() {
		writeAnthropicContentValue(jw, content)
		return nil
	}
	var collector userPartCollector
	content.ForEach(func(_, part gjson.Result) bool {
		return anthropicBlockFromOpenAIPart(&collector, part)
	})
	blocks, err := collector.finish(FormatOpenAI, "anthropic", "no Anthropic equivalent; the part would be dropped")
	if err != nil {
		return err
	}
	jw.Arr()
	for _, b := range blocks {
		jw.Raw(b)
	}
	jw.EndArr()
	return nil
}

func anthropicBlockFromOpenAIPart(c *userPartCollector, part gjson.Result) bool {
	partType := part.Get("type").String()
	switch partType {
	case "text":
		block := newJSONWriter()
		writeAnthropicTextBlock(block, part.Get("text").String())
		return c.add(string(block.Bytes()), nil)
	case "image_url":
		if urlStr := part.Get("image_url.url").String(); urlStr != "" {
			return c.add(buildAnthropicImageBlock(urlStr), nil)
		}
		return c.skip(partType)
	case "file":
		return c.add(buildAnthropicDocumentFromOpenAIFile(part.Get("file")))
	case "input_audio":
		return c.fail(newUnsupportedUserPart(FormatOpenAI, "anthropic", partType, "Anthropic Messages has no audio input block"))
	default:
		return c.skip(partType)
	}
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

func buildAnthropicDocumentFromOpenAIFile(file gjson.Result) (string, error) {
	mime, data, ok := parseDataURL(file.Get("file_data").String())
	if !ok {
		reason := "file part has no inline data URL file_data"
		if file.Get("file_id").String() != "" {
			reason = "only bare file_id without inline file_data; cross-provider file ids cannot be forwarded"
		}
		return "", newUnsupportedUserPart(FormatOpenAI, "anthropic", "file", reason)
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

func openAIUserPartsGJSON(content gjson.Result) ([]string, error) {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil, nil
		}
		return []string{geminiTextPart(s)}, nil
	case gjson.JSON:
		var collector userPartCollector
		content.ForEach(func(_, part gjson.Result) bool {
			return geminiPartFromOpenAI(&collector, part)
		})
		return collector.finish(FormatOpenAI, "gemini", "no Gemini equivalent; the part would be dropped")
	default:
	}
	return nil, nil
}

func geminiPartFromOpenAI(c *userPartCollector, part gjson.Result) bool {
	partType := part.Get("type").String()
	switch partType {
	case "text":
		if s := part.Get("text").String(); s != "" {
			return c.add(geminiTextPart(s), nil)
		}
		return true
	case "image_url":
		mime, data, ok := parseDataURL(part.Get("image_url.url").String())
		if !ok || !isValidBase64(data) {
			return c.skip(partType)
		}
		if mime == "" {
			mime = "image/jpeg"
		}
		return c.add(geminiInlineDataPart(mime, data), nil)
	case "file":
		return c.add(geminiInlineDataFromOpenAIFile(part.Get("file")))
	case "input_audio":
		return c.add(geminiInlineDataFromOpenAIAudio(part.Get("input_audio")))
	default:
		return c.skip(partType)
	}
}

func geminiInlineDataFromOpenAIFile(file gjson.Result) (string, error) {
	mime, data, ok := parseDataURL(file.Get("file_data").String())
	if !ok {
		reason := "file part has no inline data URL file_data"
		if file.Get("file_id").String() != "" {
			reason = "only bare file_id without inline file_data; cross-provider file ids cannot be forwarded"
		}
		return "", newUnsupportedUserPart(FormatOpenAI, "gemini", "file", reason)
	}
	if !isValidBase64(data) {
		return "", newUnsupportedUserPart(FormatOpenAI, "gemini", "file", "file_data is not valid base64")
	}
	if mime == "" {
		mime = "application/pdf"
	}
	return geminiInlineDataPart(mime, data), nil
}

func geminiInlineDataFromOpenAIAudio(audio gjson.Result) (string, error) {
	data := audio.Get("data").String()
	if data == "" || !isValidBase64(data) {
		return "", newUnsupportedUserPart(FormatOpenAI, "gemini", "input_audio", "input_audio.data is missing or not valid base64")
	}
	mime := "audio/wav"
	if strings.EqualFold(audio.Get("format").String(), "mp3") {
		mime = "audio/mpeg"
	}
	return geminiInlineDataPart(mime, data), nil
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

func anthropicUserPartsGJSON(content gjson.Result, toolNames map[string]string) ([]string, error) {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil, nil
		}
		return []string{geminiTextPart(s)}, nil
	case gjson.JSON:
		var collector userPartCollector
		content.ForEach(func(_, block gjson.Result) bool {
			return geminiPartFromAnthropic(&collector, block, toolNames)
		})
		return collector.finish(FormatAnthropic, "gemini", "no Gemini equivalent; the part would be dropped")
	default:
	}
	return nil, nil
}

func geminiPartFromAnthropic(c *userPartCollector, block gjson.Result, toolNames map[string]string) bool {
	blockType := block.Get("type").String()
	switch blockType {
	case "text":
		if text := block.Get("text").String(); text != "" {
			return c.add(geminiTextPart(text), nil)
		}
		return true
	case "image":
		data := block.Get("source.data").String()
		if block.Get("source.type").String() != "base64" || data == "" {
			return c.skip(blockType)
		}
		mime := block.Get("source.media_type").String()
		if mime == "" {
			mime = "image/jpeg"
		}
		return c.add(geminiInlineDataPart(mime, data), nil)
	case "document":
		return c.add(geminiInlineDataFromAnthropicDocument(block))
	case "container_upload":
		return c.fail(newUnsupportedUserPart(FormatAnthropic, "gemini", "container_upload", "cross-provider file ids cannot be forwarded"))
	case "tool_result":
		name := toolNames[block.Get("tool_use_id").String()]
		if name == "" {
			return true
		}
		return c.add(geminiFunctionResponsePart(name, toolResultContentGJSON(block.Get("content"))), nil)
	default:
		return c.skip(blockType)
	}
}

func anthropicDocumentSourceError(block gjson.Result, target string) error {
	src := block.Get("source")
	data := src.Get("data").String()
	if block.Get("citations.enabled").Bool() {
		return newUnsupportedUserPart(FormatAnthropic, target, "document", "document citations cannot be represented on this target")
	}
	switch src.Get("type").String() {
	case "base64":
		if data == "" || !isValidBase64(data) {
			return newUnsupportedUserPart(FormatAnthropic, target, "document", "document source.data is missing or not valid base64")
		}
	case "text":
		if data == "" {
			return newUnsupportedUserPart(FormatAnthropic, target, "document", "text document source.data is missing")
		}
	case "url":
		return newUnsupportedUserPart(FormatAnthropic, target, "document", "url-source documents cannot be fetched by the router")
	default:
		return newUnsupportedUserPart(FormatAnthropic, target, "document", "only base64 and text document sources can be translated")
	}
	return nil
}

func anthropicTextDocumentContent(block gjson.Result) string {
	var b strings.Builder
	if title := block.Get("title").String(); title != "" {
		b.WriteString("Document title: " + title + "\n")
	}
	if context := block.Get("context").String(); context != "" {
		b.WriteString("Document context: " + context + "\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(block.Get("source.data").String())
	return b.String()
}

func geminiInlineDataFromAnthropicDocument(block gjson.Result) (string, error) {
	if err := anthropicDocumentSourceError(block, "gemini"); err != nil {
		return "", err
	}
	source := block.Get("source")
	data := source.Get("data").String()
	if source.Get("type").String() == "text" {
		return geminiTextPart(anthropicTextDocumentContent(block)), nil
	}
	mime := source.Get("media_type").String()
	if mime == "" {
		mime = "application/pdf"
	}
	return geminiInlineDataPart(mime, data), nil
}

func openAIPartFromAnthropicBlock(c *userPartCollector, toolResultRaws *[]string, block gjson.Result) bool {
	blockType := block.Get("type").String()
	switch blockType {
	case "tool_result":
		*toolResultRaws = append(*toolResultRaws, buildOpenAIToolResultMessage(block))
		block.Get("content").ForEach(func(_, inner gjson.Result) bool {
			if inner.Get("type").String() == "image" {
				if part := buildOpenAIImagePart(inner); part != "" {
					c.parts = append(c.parts, part)
				}
			}
			return true
		})
		return true
	case "text":
		return c.add(buildOpenAITextPart(block.Get("text").String()), nil)
	case "image":
		if part := buildOpenAIImagePart(block); part != "" {
			return c.add(part, nil)
		}
		return c.skip(blockType)
	case "document":
		return c.add(buildOpenAIFilePartFromAnthropicDocument(block))
	case "container_upload":
		return c.fail(newUnsupportedUserPart(FormatAnthropic, "openai", "container_upload", "cross-provider file ids cannot be forwarded"))
	default:
		return c.skip(blockType)
	}
}

func buildOpenAITextPart(text string) string {
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("text")
	inner.Key("text")
	inner.Str(text)
	inner.EndObj()
	return string(inner.Bytes())
}

func buildOpenAIFilePartFromAnthropicDocument(block gjson.Result) (string, error) {
	if err := anthropicDocumentSourceError(block, "openai"); err != nil {
		return "", err
	}
	src := block.Get("source")
	data := src.Get("data").String()
	if src.Get("type").String() == "text" {
		return buildOpenAITextPart(anthropicTextDocumentContent(block)), nil
	}
	mediaType := src.Get("media_type").String()
	if mediaType == "" {
		mediaType = "application/pdf"
	}
	filename := block.Get("title").String()
	if filename == "" && mediaType == "application/pdf" {
		filename = "document.pdf"
	}
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("file")
	inner.Key("file")
	inner.Obj()
	if filename != "" {
		inner.Key("filename")
		inner.Str(filename)
	}
	inner.Key("file_data")
	inner.Str("data:" + mediaType + ";base64," + data)
	inner.EndObj()
	inner.EndObj()
	return string(inner.Bytes()), nil
}
