package translate

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEffortForBudget_AnthropicTierBoundaries(t *testing.T) {
	cases := map[int64]string{
		0:     "medium",
		1:     effortLow,
		4096:  effortLow,
		4097:  "medium",
		16384: "medium",
		16385: "high",
	}
	for budget, want := range cases {
		assert.Equal(t, want, effortForBudget(budget), "budget_tokens=%d", budget)
	}
}

func TestProviderUsageState_BoundSourceOwnsFacts(t *testing.T) {
	var s providerUsageState
	s.facts = usage.Facts{Present: false}
	s.SetUsageSource(func() usage.Facts { return usage.Facts{Present: true} })
	assert.True(t, s.UsageFacts().Present, "a downstream encoder reports its provider-side owner's usage")
}

func TestCarriesToolCall_RecognizesBothWireShapes(t *testing.T) {
	assert.True(t, carriesToolCall(gjson.Parse(`{"role":"assistant","tool_calls":[{"id":"c1"}]}`)))
	assert.True(t, carriesToolCall(gjson.Parse(`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]}`)))
	assert.False(t, carriesToolCall(gjson.Parse(`{"role":"assistant","tool_calls":[],"content":"ok"}`)))
}

func TestSeedToolCallGate_CaptureCapReleasesHeldText(t *testing.T) {
	var g seedToolCallGate
	assert.Empty(t, g.Feed(seedToolCallOpen))
	filler := strings.Repeat("x", seedToolCallCaptureCap)
	released := g.Feed(filler)
	assert.Equal(t, seedToolCallOpen+filler, released, "a capture past the cap must stop holding text")
}

func TestEmbedSignatureInID_IsIdempotent(t *testing.T) {
	once := embedSignatureInID("toolu_1", "sig")
	assert.Equal(t, once, embedSignatureInID(once, "other"), "an id that already carries a signature is never re-embedded")
	reasoning := embedOpenAIReasoningSignatureInID("rs_1", "sig")
	assert.Equal(t, reasoning, embedOpenAIReasoningSignatureInID(reasoning, "other"))
}

func TestDecodeOpenAIReasoningSignature_RejectsForeignProvider(t *testing.T) {
	sig := encodeOpenAIReasoningSignature("rs_1", "enc", "scope")
	_, ok := decodeOpenAIReasoningSignature(sig)
	assert.True(t, ok)
	env, _ := decodeOpenAIReasoningSignature(sig)
	env.Provider = "anthropic"
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	_, ok = decodeOpenAIReasoningSignature(base64.StdEncoding.EncodeToString(raw))
	assert.False(t, ok, "only router-minted OpenAI envelopes are replayable")
}

func TestParseDataURL_RequiresPayload(t *testing.T) {
	_, _, ok := parseDataURL("data:image/png;base64,")
	assert.False(t, ok)
	mime, data, ok := parseDataURL("data:image/png;base64,AAAA")
	assert.True(t, ok)
	assert.Equal(t, "image/png", mime)
	assert.Equal(t, "AAAA", data)
}

func TestAnthropicMediaRequirements_InputAudioCounts(t *testing.T) {
	audio, files := anthropicMediaRequirements([]byte(`{"messages":[{"role":"user","content":[{"type":"input_audio"}]}]}`))
	assert.True(t, audio)
	assert.False(t, files)
}

func TestWithAutonomySystemAppended_NonAnthropicSourceIsIdentity(t *testing.T) {
	env, err := ParseOpenAI([]byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	out, err := env.withAutonomySystemAppended(EmitOptions{AppendAutonomySystem: true})
	require.NoError(t, err)
	assert.Same(t, env, out)
}

func TestAnthropicPassthroughHeadersForBody_KeepsClientVersion(t *testing.T) {
	in := http.Header{}
	in.Set("anthropic-version", "2099-01-01")
	assert.Equal(t, "2099-01-01", AnthropicPassthroughHeadersForBody(in, []byte(`{}`)).Get("anthropic-version"))
	assert.Equal(t, "2023-06-01", AnthropicPassthroughHeadersForBody(http.Header{}, []byte(`{}`)).Get("anthropic-version"))
}

func TestGeminiRoutingMarkerWriter_EmptyMarkerEmitsCommentOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewGeminiRoutingMarkerWriter(rec, "")
	require.NoError(t, w.Prelude(true))
	assert.Equal(t, ": routing complete\n\n", rec.Body.String())
}
