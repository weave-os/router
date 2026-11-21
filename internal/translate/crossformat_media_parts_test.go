package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	mediaPartsPNG   = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAALrAfWPWZdbAAAAAElFTkSuQmCC"
	mediaPartsPDF   = "JVBERi0xLjQKJSVFT0YK"
	mediaPartsWAV   = "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="
	mediaPartsText  = "See attached."
	mediaPartsFile  = "file-abc123"
	anthropicTarget = "claude-sonnet-4-20250514"
	geminiTarget    = "gemini-2.5-pro"
	openAITarget    = "gpt-4o"
)

func openAIUserBody(parts string) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":[` + parts + `]}]}`)
}

func anthropicUserBody(parts string) []byte {
	return []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[` + parts + `]}]}`)
}

const (
	openAITextPart     = `{"type":"text","text":"` + mediaPartsText + `"}`
	openAIImagePart    = `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + mediaPartsPNG + `"}}`
	openAIFilePDFPart  = `{"type":"file","file":{"file_data":"data:application/pdf;base64,` + mediaPartsPDF + `","filename":"doc.pdf"}}`
	openAIFileIDPart   = `{"type":"file","file":{"file_id":"` + mediaPartsFile + `"}}`
	openAIInputWAVPart = `{"type":"input_audio","input_audio":{"data":"` + mediaPartsWAV + `","format":"wav"}}`

	anthropicTextPart        = `{"type":"text","text":"` + mediaPartsText + `"}`
	anthropicImagePart       = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + mediaPartsPNG + `"}}`
	anthropicDocumentPDFPart = `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + mediaPartsPDF + `"}}`
	anthropicContainerUpload = `{"type":"container_upload","file_id":"` + mediaPartsFile + `"}`
)

func openAIToAnthropic(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: anthropicTarget})
	return prep.Body, err
}

func openAIToGemini(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: geminiTarget})
	return prep.Body, err
}

func anthropicToOpenAI(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: openAITarget})
	return prep.Body, err
}

func anthropicToGemini(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: geminiTarget})
	return prep.Body, err
}

func requireUntranslatable(t *testing.T, body []byte, err error, detail string) {
	t.Helper()
	require.ErrorIs(t, err, translate.ErrUntranslatableUserContent)
	assert.Contains(t, err.Error(), detail)
	assert.Empty(t, body, "a rejected request must not produce a body to forward")
}

func assertGeminiTextPart(t *testing.T, part gjson.Result) {
	t.Helper()
	assert.Equal(t, mediaPartsText, part.Get("text").String(), part.Raw)
}

func assertGeminiInlineData(t *testing.T, part gjson.Result, mime, data string) {
	t.Helper()
	assert.Equal(t, mime, part.Get("inlineData.mimeType").String(), part.Raw)
	assert.Equal(t, data, part.Get("inlineData.data").String(), part.Raw)
}

func TestCrossFormat_OpenAIToAnthropic_FileIDRejected(t *testing.T) {
	t.Run("Case1_openai_file_id_to_anthropic", func(t *testing.T) {
		body, err := openAIToAnthropic(t, openAIUserBody(openAIFileIDPart))
		requireUntranslatable(t, body, err, "file_id")
	})
}

func TestCrossFormat_OpenAIToAnthropic_InputAudioRejected(t *testing.T) {
	t.Run("Case2_openai_input_audio_to_anthropic", func(t *testing.T) {
		body, err := openAIToAnthropic(t, openAIUserBody(openAIInputWAVPart))
		requireUntranslatable(t, body, err, "input_audio")
	})
}

func TestCrossFormat_OpenAIToAnthropic_FilePDFPreserved(t *testing.T) {
	t.Run("Case3_openai_file_pdf_to_anthropic_document", func(t *testing.T) {
		body, err := openAIToAnthropic(t, openAIUserBody(openAITextPart+","+openAIImagePart+","+openAIFilePDFPart+","+openAITextPart))
		require.NoError(t, err)

		messages := gjson.GetBytes(body, "messages").Array()
		require.Len(t, messages, 1, string(body))
		blocks := messages[0].Get("content").Array()
		require.Len(t, blocks, 4, string(body))

		assert.Equal(t, "text", blocks[0].Get("type").String())
		assert.Equal(t, mediaPartsText, blocks[0].Get("text").String())
		assert.Equal(t, "image", blocks[1].Get("type").String())
		assert.Equal(t, "image/png", blocks[1].Get("source.media_type").String())
		assert.Equal(t, mediaPartsPNG, blocks[1].Get("source.data").String())
		assert.Equal(t, "document", blocks[2].Get("type").String())
		assert.Equal(t, "base64", blocks[2].Get("source.type").String())
		assert.Equal(t, "application/pdf", blocks[2].Get("source.media_type").String())
		assert.Equal(t, mediaPartsPDF, blocks[2].Get("source.data").String())
		assert.Equal(t, "doc.pdf", blocks[2].Get("title").String())
		assert.Equal(t, "text", blocks[3].Get("type").String())
		assert.Equal(t, mediaPartsText, blocks[3].Get("text").String())
	})
}

func TestCrossFormat_OpenAIToGemini_FileIDRejected(t *testing.T) {
	t.Run("Case4_openai_file_id_to_gemini", func(t *testing.T) {
		body, err := openAIToGemini(t, openAIUserBody(openAIFileIDPart))
		requireUntranslatable(t, body, err, "file_id")
	})
}

func TestCrossFormat_OpenAIToGemini_InputAudioWAVPreserved(t *testing.T) {
	t.Run("Case5_openai_input_audio_to_gemini_inline_data", func(t *testing.T) {
		body, err := openAIToGemini(t, openAIUserBody(openAIInputWAVPart))
		require.NoError(t, err)

		contents := gjson.GetBytes(body, "contents").Array()
		require.Len(t, contents, 1, string(body))
		assert.Equal(t, "user", contents[0].Get("role").String())
		parts := contents[0].Get("parts").Array()
		require.Len(t, parts, 1, string(body))
		assertGeminiInlineData(t, parts[0], "audio/wav", mediaPartsWAV)
	})
}

func TestCrossFormat_OpenAIToGemini_FilePDFPreserved(t *testing.T) {
	t.Run("Case6_openai_file_pdf_to_gemini_inline_data", func(t *testing.T) {
		body, err := openAIToGemini(t, openAIUserBody(openAITextPart+","+openAIImagePart+","+openAIFilePDFPart+","+openAITextPart))
		require.NoError(t, err)

		contents := gjson.GetBytes(body, "contents").Array()
		require.Len(t, contents, 1, string(body))
		parts := contents[0].Get("parts").Array()
		require.Len(t, parts, 4, string(body))
		assertGeminiTextPart(t, parts[0])
		assertGeminiInlineData(t, parts[1], "image/png", mediaPartsPNG)
		assertGeminiInlineData(t, parts[2], "application/pdf", mediaPartsPDF)
		assertGeminiTextPart(t, parts[3])
	})
}

func TestCrossFormat_AnthropicToOpenAI_ContainerUploadRejected(t *testing.T) {
	t.Run("Case7_anthropic_container_upload_to_openai", func(t *testing.T) {
		body, err := anthropicToOpenAI(t, anthropicUserBody(anthropicContainerUpload))
		requireUntranslatable(t, body, err, "container_upload")
	})
}

func TestCrossFormat_AnthropicToOpenAI_DocumentPDFPreserved(t *testing.T) {
	t.Run("Case8_anthropic_document_pdf_to_openai_file", func(t *testing.T) {
		body, err := anthropicToOpenAI(t, anthropicUserBody(anthropicDocumentPDFPart))
		require.NoError(t, err)

		messages := gjson.GetBytes(body, "messages").Array()
		require.Len(t, messages, 1, string(body))
		assert.Equal(t, "user", messages[0].Get("role").String())
		parts := messages[0].Get("content").Array()
		require.Len(t, parts, 1, string(body))
		assert.Equal(t, "file", parts[0].Get("type").String())
		assert.Equal(t, "data:application/pdf;base64,"+mediaPartsPDF, parts[0].Get("file.file_data").String())
		assert.Equal(t, "document.pdf", parts[0].Get("file.filename").String())
	})
	for title, want := range map[string]string{"Quarterly report": "Quarterly report.pdf", "report.PDF": "report.PDF"} {
		t.Run("Case8_titled_document_filename_"+want, func(t *testing.T) {
			titled := `{"type":"document","title":"` + title + `","source":{"type":"base64","media_type":"application/pdf","data":"` + mediaPartsPDF + `"}}`
			body, err := anthropicToOpenAI(t, anthropicUserBody(titled))
			require.NoError(t, err)
			assert.Equal(t, want, gjson.GetBytes(body, "messages.0.content.0.file.filename").String(), string(body))
		})
	}
}

func TestCrossFormat_AnthropicToOpenAI_DocumentPDFWithNeighbours(t *testing.T) {
	t.Run("Case9_anthropic_document_pdf_with_neighbours_to_openai", func(t *testing.T) {
		body, err := anthropicToOpenAI(t, anthropicUserBody(anthropicTextPart+","+anthropicImagePart+","+anthropicDocumentPDFPart+","+anthropicTextPart))
		require.NoError(t, err)

		messages := gjson.GetBytes(body, "messages").Array()
		require.Len(t, messages, 1, string(body))
		parts := messages[0].Get("content").Array()
		require.Len(t, parts, 4, string(body))

		assert.Equal(t, "text", parts[0].Get("type").String())
		assert.Equal(t, mediaPartsText, parts[0].Get("text").String())
		assert.Equal(t, "image_url", parts[1].Get("type").String())
		assert.Equal(t, "data:image/png;base64,"+mediaPartsPNG, parts[1].Get("image_url.url").String())
		assert.Equal(t, "file", parts[2].Get("type").String())
		assert.Equal(t, "data:application/pdf;base64,"+mediaPartsPDF, parts[2].Get("file.file_data").String())
		assert.Equal(t, "text", parts[3].Get("type").String())
		assert.Equal(t, mediaPartsText, parts[3].Get("text").String())
	})
}

func TestCrossFormat_AnthropicToGemini_ContainerUploadRejected(t *testing.T) {
	t.Run("Case10_anthropic_container_upload_to_gemini", func(t *testing.T) {
		body, err := anthropicToGemini(t, anthropicUserBody(anthropicContainerUpload))
		requireUntranslatable(t, body, err, "container_upload")
	})
}

func TestCrossFormat_AnthropicToGemini_DocumentPDFPreserved(t *testing.T) {
	t.Run("Case11_anthropic_document_pdf_to_gemini_inline_data", func(t *testing.T) {
		body, err := anthropicToGemini(t, anthropicUserBody(anthropicDocumentPDFPart))
		require.NoError(t, err)

		contents := gjson.GetBytes(body, "contents").Array()
		require.Len(t, contents, 1, string(body))
		assert.Equal(t, "user", contents[0].Get("role").String())
		parts := contents[0].Get("parts").Array()
		require.Len(t, parts, 1, string(body))
		assertGeminiInlineData(t, parts[0], "application/pdf", mediaPartsPDF)
	})
}

func TestCrossFormat_AnthropicToGemini_DocumentPDFWithNeighbours(t *testing.T) {
	t.Run("Case12_anthropic_document_pdf_with_neighbours_to_gemini", func(t *testing.T) {
		body, err := anthropicToGemini(t, anthropicUserBody(anthropicTextPart+","+anthropicImagePart+","+anthropicDocumentPDFPart+","+anthropicTextPart))
		require.NoError(t, err)

		contents := gjson.GetBytes(body, "contents").Array()
		require.Len(t, contents, 1, string(body))
		parts := contents[0].Get("parts").Array()
		require.Len(t, parts, 4, string(body))
		assertGeminiTextPart(t, parts[0])
		assertGeminiInlineData(t, parts[1], "image/png", mediaPartsPNG)
		assertGeminiInlineData(t, parts[2], "application/pdf", mediaPartsPDF)
		assertGeminiTextPart(t, parts[3])
	})
}
