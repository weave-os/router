package translate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBase64SignatureBytes sums the byte length of every base64
// thought-signature payload and ignores everything else.
func TestBase64SignatureBytes(t *testing.T) {
	assert.Equal(t, 0, base64SignatureBytes([]byte(`{"messages":[]}`)), "no signature field")
	assert.Equal(t, 6, base64SignatureBytes([]byte(`{"signature":"ABCabc"}`)), "single payload counts its base64 bytes only")
	assert.Equal(t, 10, base64SignatureBytes([]byte(`{"signature":"AAAA"},{"signature":"BBBBBB"}`)), "two payloads sum (4 + 6)")
	// A marker with no closing quote is not counted (truncated / malformed).
	assert.Equal(t, 0, base64SignatureBytes([]byte(`{"signature":"AAAA`)), "unterminated payload is skipped")
}

// TestContextOverflowTokenEstimate_FullBody divides the full body bytes
// (signatures included — the count a signature-keeping target receives) by the
// dense-content ratio.
func TestContextOverflowTokenEstimate_FullBody(t *testing.T) {
	body := []byte(strings.Repeat("x", 400))
	e := &RequestEnvelope{body: body, format: FormatAnthropic}
	assert.Equal(t, 100, e.ContextOverflowTokenEstimate(), "400 bytes / 4 = 100 tokens")
}

// TestSignatureTokenSavings returns the token savings a signature-stripping
// target gets from dropping the base64 payloads — but only for Anthropic-format
// input; other formats carry no Anthropic thought-signatures to strip.
func TestSignatureTokenSavings(t *testing.T) {
	sig := strings.Repeat("A", 800)
	body := []byte(`{"content":"` + strings.Repeat("x", 400) + `","signature":"` + sig + `"}`)

	anthropic := &RequestEnvelope{body: body, format: FormatAnthropic}
	assert.Equal(t, 200, anthropic.SignatureTokenSavings(), "800 signature bytes / 4 = 200 tokens saved")

	// Same bytes arriving as an OpenAI body: the "signature" field is caller
	// data, not an Anthropic block, so nothing is stripped and nothing is saved.
	openai := &RequestEnvelope{body: body, format: FormatOpenAI}
	assert.Equal(t, 0, openai.SignatureTokenSavings(), "non-Anthropic format saves nothing")
}

// TestContextOverflowTokenEstimate_TicketRegression is the regression for the
// 262K-overflow ticket: a signature-light, content-dense ~1.05MB body is a real
// ~263K-token prompt. The old ÷6 estimate (~175K) let it pass the pre-filter
// onto a 256K OSS model, which then hard-400'd on context overflow. The
// strip-aware ÷4 estimate must land above that window so the model is excluded.
func TestContextOverflowTokenEstimate_TicketRegression(t *testing.T) {
	const kimiWindow = 262_143
	body := []byte(strings.Repeat("x", 1_050_000))
	e := &RequestEnvelope{body: body, format: FormatAnthropic}

	assert.Greater(t, e.ContextOverflowTokenEstimate(), kimiWindow, "dense ~263K-token body must estimate above a 256K window")
	assert.Less(t, e.FullTokenEstimate(), kimiWindow, "the old ÷6 estimate undercounted below the window — the bug this fixes")
}

// TestBase64ImageStats sums inline base64 image payloads per inbound format,
// covering top-level and tool_result-nested Anthropic images, OpenAI data URLs
// (http URLs skipped), and Gemini inlineData (camelCase + snake_case).
func TestBase64ImageStats(t *testing.T) {
	anthropic := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","data":"AAAA"}},` +
		`{"type":"tool_result","content":[{"type":"image","source":{"data":"BBBBBB"}}]}]}]}`)
	b, c := (&RequestEnvelope{body: anthropic, format: FormatAnthropic}).base64ImageStats()
	assert.Equal(t, 10, b, "top-level (4) + tool_result-nested (6) image bytes")
	assert.Equal(t, 2, c, "counts both images")

	openai := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,ABCDEFGH"}},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/y.png"}}]}]}`)
	b, c = (&RequestEnvelope{body: openai, format: FormatOpenAI}).base64ImageStats()
	assert.Equal(t, 8, b, "only the data-URL base64 payload counts")
	assert.Equal(t, 1, c, "http image URL is not an in-body payload")

	gemini := []byte(`{"contents":[{"parts":[` +
		`{"inlineData":{"mimeType":"image/png","data":"AAAA"}},` +
		`{"inline_data":{"data":"BB"}}]}]}`)
	b, c = (&RequestEnvelope{body: gemini, format: FormatGemini}).base64ImageStats()
	assert.Equal(t, 6, b, "camelCase (4) + snake_case (2) inlineData bytes")
	assert.Equal(t, 2, c, "counts both inline parts")
}

// TestContextOverflowTokenEstimate_ImagesRepriced is the regression for
// phantom token inflation on multi-page PDF reads: base64 transport size
// must not be counted at the content byte ratio.
func TestContextOverflowTokenEstimate_ImagesRepriced(t *testing.T) {
	const pages = 20
	const pageBytes = 250_000 // ~250KB base64 per rendered page
	page := strings.Repeat("A", pageBytes)
	blocks := make([]string, pages)
	for i := range blocks {
		blocks[i] = `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + page + `"}}`
	}
	body := []byte(`{"messages":[{"role":"user","content":[` + strings.Join(blocks, ",") +
		`,{"type":"text","text":"summarize this paper"}]}]}`)
	e := &RequestEnvelope{body: body, format: FormatAnthropic}

	imgBytes, imgCount := e.base64ImageStats()
	assert.Equal(t, pages, imgCount, "counts one image per page")
	assert.Equal(t, pages*pageBytes, imgBytes, "sums each page's base64 bytes")

	// The naive len/4 estimate is a phantom >1M-token overflow (the bug).
	assert.Greater(t, len(body)/contentBytesPerToken, 1_000_000, "raw body ÷4 falsely overflows")
	// Repriced, the same body is well below any compaction trigger.
	assert.Less(t, e.ContextOverflowTokenEstimate(), 100_000, "repriced estimate stays far below the window")
}

// TestContextOverflowTokenEstimate_ExcludesToolIDCarriers is the regression
// for Claude Code sessions served by GPT reasoning models: every echoed tool
// id carries ~8KB of router-minted reasoning, which inflated a ~20K-token
// request to a multi-million-token estimate and tripped the compaction cascade.
func TestContextOverflowTokenEstimate_ExcludesToolIDCarriers(t *testing.T) {
	blob := strings.Repeat("A", 8_000)
	var sb strings.Builder
	sb.WriteString(`{"messages":[`)
	for i := range 200 {
		if i > 0 {
			sb.WriteString(",")
		}
		id := "call_" + strings.Repeat("x", 4) + openAIReasoningSignatureIDDelimiter + blob
		sb.WriteString(`{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":{}}]},`)
		sb.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}`)
	}
	sb.WriteString(`]}`)
	body := []byte(sb.String())
	e := &RequestEnvelope{body: body, format: FormatAnthropic}

	assert.Greater(t, len(body)/contentBytesPerToken, 800_000, "raw ÷4 counts every echoed carrier")
	assert.Less(t, e.ContextOverflowTokenEstimate(), 20_000, "carriers are transport, not prompt tokens")
	assert.Less(t, e.FullTokenEstimate(), 20_000)
}

func TestOpenAIReasoningIDBytes(t *testing.T) {
	marker := len(openAIReasoningSignatureIDDelimiter)
	assert.Equal(t, 0, openAIReasoningIDBytes([]byte(`{"id":"toolu_1"}`)), "plain id has no carrier")
	assert.Equal(t, 0, openAIReasoningIDBytes([]byte(`{"id":"t`+thoughtSignatureIDDelimiter+`QUJD"}`)), "Gemini carrier reaches Anthropic and stays counted")
	two := `{"id":"a` + openAIReasoningSignatureIDDelimiter + `XX"},{"tool_use_id":"a` + openAIReasoningSignatureIDDelimiter + `YYY"}`
	assert.Equal(t, 2*marker+5, openAIReasoningIDBytes([]byte(two)), "both ends of a pair count")
	assert.Equal(t, 0, openAIReasoningIDBytes([]byte(`{"id":"a`+openAIReasoningSignatureIDDelimiter+`XX`)), "unterminated carrier is skipped")
}

func TestRouterMintedSignaturesExcludedFromEstimate(t *testing.T) {
	minted := encodeOpenAIReasoningSignature("rs_1", strings.Repeat("E", 6000), "scope")
	require.True(t, strings.HasPrefix(minted, string(routerMintedSignaturePrefix)), "prefix must track the envelope encoding")
	anthropicSig := strings.Repeat("S", 800)
	text := strings.Repeat("x", 400)
	body := []byte(`{"messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":"` + minted + `"},` +
		`{"type":"thinking","thinking":"","signature":"` + anthropicSig + `"},` +
		`{"type":"text","text":"` + text + `"}]}]}`)
	e := &RequestEnvelope{body: body, format: FormatAnthropic}

	all, routerMinted := signatureBytes(body)
	assert.Equal(t, len(minted)+len(anthropicSig), all)
	assert.Equal(t, len(minted), routerMinted)
	assert.Equal(t, (len(body)-len(minted))/contentBytesPerToken, e.ContextOverflowTokenEstimate(), "router-minted signature is never dispatched as prompt")
	assert.Equal(t, len(anthropicSig)/contentBytesPerToken, e.SignatureTokenSavings(), "only real Anthropic signatures remain to be saved")
}
