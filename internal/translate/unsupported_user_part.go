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
		c.skippedType = partType
	}
	return true
}

func (c *userPartCollector) result(src Format, target, reason string) ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	if len(c.parts) == 0 && c.skippedType != "" {
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
	blocks, err := collector.result(FormatOpenAI, "anthropic", "no Anthropic equivalent; the message would be sent with no content")
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
		return collector.result(FormatOpenAI, "gemini", "no Gemini equivalent; the message would be sent with no content")
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
		return collector.result(FormatAnthropic, "gemini", "no Gemini equivalent; the message would be sent with no content")
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
		return c.add(geminiInlineDataFromAnthropicDocument(block.Get("source")))
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

func geminiInlineDataFromAnthropicDocument(source gjson.Result) (string, error) {
	if source.Get("type").String() != "base64" {
		return "", newUnsupportedUserPart(FormatAnthropic, "gemini", "document", "url-source documents cannot be fetched by the router")
	}
	data := source.Get("data").String()
	if data == "" || !isValidBase64(data) {
		return "", newUnsupportedUserPart(FormatAnthropic, "gemini", "document", "document source.data is missing or not valid base64")
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
		inner := newJSONWriter()
		inner.Obj()
		inner.Key("type")
		inner.Str("text")
		inner.Key("text")
		inner.Str(block.Get("text").String())
		inner.EndObj()
		return c.add(string(inner.Bytes()), nil)
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

func buildOpenAIFilePartFromAnthropicDocument(block gjson.Result) (string, error) {
	src := block.Get("source")
	if src.Get("type").String() != "base64" {
		return "", newUnsupportedUserPart(FormatAnthropic, "openai", "document", "url-source documents cannot be fetched by the router")
	}
	data := src.Get("data").String()
	if data == "" || !isValidBase64(data) {
		return "", newUnsupportedUserPart(FormatAnthropic, "openai", "document", "document source.data is missing or not valid base64")
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
