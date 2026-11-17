package recordedlaws_test

import (
	"github.com/tidwall/gjson"
)

var geminiFinishReasons = map[string]bool{"STOP": true, "MAX_TOKENS": true, "SAFETY": true, "RECITATION": true, "OTHER": true, "MALFORMED_FUNCTION_CALL": true}

func validateGeminiParts(p *problems, where string, parts gjson.Result) {
	for i, part := range parts.Array() {
		known := part.Get("text").Exists() || part.Get("functionCall").Exists() || part.Get("functionResponse").Exists() || part.Get("inlineData").Exists()
		p.require(known, "%s part %d carries no known payload", where, i)
		if fc := part.Get("functionCall"); fc.Exists() {
			p.require(fc.Get("name").String() != "", "%s part %d functionCall has no name", where, i)
			p.require(!fc.Get("args").Exists() || fc.Get("args").IsObject(), "%s part %d functionCall args are not an object", where, i)
		}
	}
}

func validateGeminiResponse(body gjson.Result) []string {
	var p problems
	cand := body.Get("candidates.0")
	p.require(cand.Exists(), "response has no candidates")
	p.require(cand.Get("content.role").String() == "model", "candidate role is %q, want model", cand.Get("content.role").String())
	validateGeminiParts(&p, "candidate", cand.Get("content.parts"))
	p.require(geminiFinishReasons[cand.Get("finishReason").String()], "finishReason %q is not a documented value", cand.Get("finishReason").String())
	p.require(body.Get("usageMetadata.promptTokenCount").Type == gjson.Number, "usageMetadata.promptTokenCount is missing")
	return p
}

func validateGeminiStream(body []byte) []string {
	var p problems
	for i, f := range parseSSE(body) {
		p.require(gjson.Valid(f.data) && gjson.Get(f.data, "candidates").IsArray(), "chunk %d is not a Gemini candidates chunk", i)
	}
	p = append(p, validateGeminiResponse(gjson.ParseBytes(assembleGeminiSSE(body)))...)
	return p
}
