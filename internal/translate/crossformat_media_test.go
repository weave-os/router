package translate_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

const (
	mediaTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAALrAfWPWZdbAAAAAElFTkSuQmCC"
	mediaTestPDF = "JVBERi0xLjQKJcOkw7zDtsOfCjEgMCBvYmoKPDwvVHlwZS9DYXRhbG9nPj4KZW5kb2JqCg=="
	mediaTestWAV = "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="
)

var openAIBareFileIDBody = []byte(`{"model":"m","messages":[{"role":"user","content":[
	{"type":"file","file":{"file_id":"file-abc123"}}]}]}`)

var openAIInputAudioBody = []byte(`{"model":"m","messages":[{"role":"user","content":[
	{"type":"input_audio","input_audio":{"data":"` + mediaTestWAV + `","format":"wav"}}]}]}`)

var openAITextImagePDFTextBody = []byte(`{"model":"m","messages":[{"role":"user","content":[
	{"type":"text","text":"See attached."},
	{"type":"image_url","image_url":{"url":"data:image/png;base64,` + mediaTestPNG + `"}},
	{"type":"file","file":{"file_data":"data:application/pdf;base64,` + mediaTestPDF + `","filename":"doc.pdf"}},
	{"type":"text","text":"Summarize it."}]}]}`)

var anthropicContainerUploadBody = []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[
	{"type":"container_upload","file_id":"file-abc123"}]}]}`)

var anthropicDocumentBody = []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[
	{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + mediaTestPDF + `"}}]}]}`)

var anthropicTextImageDocumentTextBody = []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[
	{"type":"text","text":"See attached."},
	{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + mediaTestPNG + `"}},
	{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + mediaTestPDF + `"}},
	{"type":"text","text":"Summarize it."}]}]}`)

var (
	anthropicTarget = translate.EmitOptions{TargetModel: "claude-sonnet-4-20250514"}
	geminiTarget    = translate.EmitOptions{TargetModel: "gemini-2.5-pro"}
	openAITarget    = translate.EmitOptions{TargetModel: "gpt-4o", TargetProvider: "openai"}
)

func requireUnsupportedUserPart(t *testing.T, body []byte, err error, partType, target string) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, translate.ErrUnsupportedUserPart)
	var typed *translate.UnsupportedUserPartError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, partType, typed.PartType)
	assert.Equal(t, target, typed.TargetFamily)
	assert.Empty(t, body, "no body may be emitted for a rejected request")
}

func singleUserContent(t *testing.T, body []byte, msgKey, contentKey string) []any {
	t.Helper()
	msgs := getArray(t, unmarshalBody(t, body), msgKey)
	require.Len(t, msgs, 1)
	msg := msgAt(t, msgs, 0)
	assert.Equal(t, "user", msg["role"])
	return mustAs[[]any](t, msg[contentKey])
}

func assertGeminiText(t *testing.T, part any, want string) {
	t.Helper()
	assert.Equal(t, want, mustAs[map[string]any](t, part)["text"])
}

func assertGeminiInlineData(t *testing.T, part any, mime, data string) {
	t.Helper()
	inline := getMap(t, mustAs[map[string]any](t, part), "inlineData")
	assert.Equal(t, mime, inline["mimeType"])
	assert.Equal(t, data, inline["data"])
}

func assertTypedText(t *testing.T, part any, want string) {
	t.Helper()
	m := mustAs[map[string]any](t, part)
	assert.Equal(t, "text", m["type"])
	assert.Equal(t, want, m["text"])
}

func assertOpenAIFilePart(t *testing.T, part any) {
	t.Helper()
	m := mustAs[map[string]any](t, part)
	assert.Equal(t, "file", m["type"])
	assert.Equal(t, "data:application/pdf;base64,"+mediaTestPDF, getMap(t, m, "file")["file_data"])
}

func TestCrossFormat_OpenAIToAnthropic_BareFileIDRejected(t *testing.T) {
	env, err := translate.ParseOpenAI(openAIBareFileIDBody)
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, anthropicTarget)
	requireUnsupportedUserPart(t, prep.Body, err, "file", "anthropic")
}

func TestCrossFormat_OpenAIToAnthropic_InputAudioRejected(t *testing.T) {
	env, err := translate.ParseOpenAI(openAIInputAudioBody)
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, anthropicTarget)
	requireUnsupportedUserPart(t, prep.Body, err, "input_audio", "anthropic")
}

func TestCrossFormat_OpenAIToAnthropic_InlinePDFFileBecomesDocument(t *testing.T) {
	env, err := translate.ParseOpenAI(openAITextImagePDFTextBody)
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, anthropicTarget)
	require.NoError(t, err)

	blocks := singleUserContent(t, prep.Body, "messages", "content")
	require.Len(t, blocks, 4)

	assertTypedText(t, blocks[0], "See attached.")

	img := mustAs[map[string]any](t, blocks[1])
	assert.Equal(t, "image", img["type"])
	imgSrc := getMap(t, img, "source")
	assert.Equal(t, "base64", imgSrc["type"])
	assert.Equal(t, "image/png", imgSrc["media_type"])
	assert.Equal(t, mediaTestPNG, imgSrc["data"])

	doc := mustAs[map[string]any](t, blocks[2])
	assert.Equal(t, "document", doc["type"])
	docSrc := getMap(t, doc, "source")
	assert.Equal(t, "base64", docSrc["type"])
	assert.Equal(t, "application/pdf", docSrc["media_type"])
	assert.Equal(t, mediaTestPDF, docSrc["data"])

	assertTypedText(t, blocks[3], "Summarize it.")
}

func TestCrossFormat_OpenAIToGemini_BareFileIDRejected(t *testing.T) {
	env, err := translate.ParseOpenAI(openAIBareFileIDBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	requireUnsupportedUserPart(t, prep.Body, err, "file", "gemini")
}

func TestCrossFormat_OpenAIToGemini_InputAudioBecomesInlineData(t *testing.T) {
	env, err := translate.ParseOpenAI(openAIInputAudioBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	require.NoError(t, err)

	parts := singleUserContent(t, prep.Body, "contents", "parts")
	require.Len(t, parts, 1)
	assertGeminiInlineData(t, parts[0], "audio/wav", mediaTestWAV)
}

func TestCrossFormat_OpenAIToGemini_InlinePDFFileBecomesInlineData(t *testing.T) {
	env, err := translate.ParseOpenAI(openAITextImagePDFTextBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	require.NoError(t, err)

	parts := singleUserContent(t, prep.Body, "contents", "parts")
	require.Len(t, parts, 4)
	assertGeminiText(t, parts[0], "See attached.")
	assertGeminiInlineData(t, parts[1], "image/png", mediaTestPNG)
	assertGeminiInlineData(t, parts[2], "application/pdf", mediaTestPDF)
	assertGeminiText(t, parts[3], "Summarize it.")
}

func TestCrossFormat_AnthropicToOpenAI_ContainerUploadRejected(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicContainerUploadBody)
	require.NoError(t, err)

	prep, err := env.PrepareOpenAI(http.Header{}, openAITarget)
	requireUnsupportedUserPart(t, prep.Body, err, "container_upload", "openai")
}

func TestCrossFormat_AnthropicToOpenAI_DocumentBecomesFilePart(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicDocumentBody)
	require.NoError(t, err)

	prep, err := env.PrepareOpenAI(http.Header{}, openAITarget)
	require.NoError(t, err)

	content := singleUserContent(t, prep.Body, "messages", "content")
	require.Len(t, content, 1)
	assertOpenAIFilePart(t, content[0])
}

func TestCrossFormat_AnthropicToOpenAI_MixedDocumentKeepsOrder(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicTextImageDocumentTextBody)
	require.NoError(t, err)

	prep, err := env.PrepareOpenAI(http.Header{}, openAITarget)
	require.NoError(t, err)

	content := singleUserContent(t, prep.Body, "messages", "content")
	require.Len(t, content, 4)

	assertTypedText(t, content[0], "See attached.")

	img := mustAs[map[string]any](t, content[1])
	assert.Equal(t, "image_url", img["type"])
	assert.Equal(t, "data:image/png;base64,"+mediaTestPNG, getMap(t, img, "image_url")["url"])

	assertOpenAIFilePart(t, content[2])
	assertTypedText(t, content[3], "Summarize it.")
}

func TestCrossFormat_AnthropicToGemini_ContainerUploadRejected(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicContainerUploadBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	requireUnsupportedUserPart(t, prep.Body, err, "container_upload", "gemini")
}

func TestCrossFormat_AnthropicToGemini_DocumentBecomesInlineData(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicDocumentBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	require.NoError(t, err)

	parts := singleUserContent(t, prep.Body, "contents", "parts")
	require.Len(t, parts, 1)
	assertGeminiInlineData(t, parts[0], "application/pdf", mediaTestPDF)
}

func TestCrossFormat_AnthropicToGemini_MixedDocumentKeepsOrder(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicTextImageDocumentTextBody)
	require.NoError(t, err)

	prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
	require.NoError(t, err)

	parts := singleUserContent(t, prep.Body, "contents", "parts")
	require.Len(t, parts, 4)
	assertGeminiText(t, parts[0], "See attached.")
	assertGeminiInlineData(t, parts[1], "image/png", mediaTestPNG)
	assertGeminiInlineData(t, parts[2], "application/pdf", mediaTestPDF)
	assertGeminiText(t, parts[3], "Summarize it.")
}
