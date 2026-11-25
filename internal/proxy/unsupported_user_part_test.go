package proxy_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"weave-os/router/internal/config"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedUserPart_ClassifiedAsClientVisible400(t *testing.T) {
	err := fmt.Errorf("prepare: %w", translate.NewUnsupportedUserPart(translate.FormatOpenAI, "anthropic", "input_audio", "anthropic has no audio input"))

	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.Equal(t, ingress.DispatchErrorTranslationIntrinsicallyIncompatible, cls.Kind)
	assert.True(t, cls.Kind.IsClientError())
	assert.Equal(t, "warn", cls.LogLevel)
	assert.Contains(t, cls.Message, "anthropic")
	assert.Contains(t, cls.Message, "input_audio")
	assert.Contains(t, cls.Message, "anthropic has no audio input")
}

func TestUnsupportedUserPart_BareSentinelStillClassified(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(fmt.Errorf("prepare: %w", translate.ErrUnsupportedUserPart))
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.NotEmpty(t, cls.Message)
}

func TestUnsupportedUserPart_DefaultCompatibilityModeRejectsAtEmit(t *testing.T) {
	t.Setenv("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "")
	mode, err := config.TranslationCompatibilityMode()
	require.NoError(t, err)
	require.Equal(t, "shadow", mode)

	openAIBareFileID := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-abc123"}}]}]}`)
	openAIInputAudio := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA=","format":"wav"}}]}]}`)
	anthropicContainerUpload := []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-abc123"}]}]}`)

	anthropicTarget := translate.EmitOptions{TargetModel: "claude-sonnet-4-20250514"}
	geminiTarget := translate.EmitOptions{TargetModel: "gemini-2.5-pro"}
	openAITarget := translate.EmitOptions{TargetModel: "gpt-4o", TargetProvider: "openai"}

	prepareOpenAI := func(body []byte, emit func(*translate.RequestEnvelope) ([]byte, error)) func(*testing.T) ([]byte, error) {
		return func(t *testing.T) ([]byte, error) {
			env, err := translate.ParseOpenAI(body)
			require.NoError(t, err)
			return emit(env)
		}
	}
	prepareAnthropic := func(body []byte, emit func(*translate.RequestEnvelope) ([]byte, error)) func(*testing.T) ([]byte, error) {
		return func(t *testing.T) ([]byte, error) {
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			return emit(env)
		}
	}
	toAnthropic := func(env *translate.RequestEnvelope) ([]byte, error) {
		prep, err := env.PrepareAnthropic(http.Header{}, anthropicTarget)
		return prep.Body, err
	}
	toGemini := func(env *translate.RequestEnvelope) ([]byte, error) {
		prep, err := env.PrepareGemini(http.Header{}, geminiTarget)
		return prep.Body, err
	}
	toOpenAI := func(env *translate.RequestEnvelope) ([]byte, error) {
		prep, err := env.PrepareOpenAI(http.Header{}, openAITarget)
		return prep.Body, err
	}

	cases := []struct {
		name     string
		prepare  func(*testing.T) ([]byte, error)
		target   string
		partType string
	}{
		{"openai_file_id_to_anthropic", prepareOpenAI(openAIBareFileID, toAnthropic), "anthropic", "file"},
		{"openai_input_audio_to_anthropic", prepareOpenAI(openAIInputAudio, toAnthropic), "anthropic", "input_audio"},
		{"openai_file_id_to_gemini", prepareOpenAI(openAIBareFileID, toGemini), "gemini", "file"},
		{"anthropic_container_upload_to_openai", prepareAnthropic(anthropicContainerUpload, toOpenAI), "openai", "container_upload"},
		{"anthropic_container_upload_to_gemini", prepareAnthropic(anthropicContainerUpload, toGemini), "gemini", "container_upload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tc.prepare(t)
			require.Error(t, err)
			require.True(t, errors.Is(err, translate.ErrUnsupportedUserPart))
			var partErr *translate.UnsupportedUserPartError
			require.ErrorAs(t, err, &partErr)
			assert.Equal(t, tc.target, partErr.TargetFamily)
			assert.Equal(t, tc.partType, partErr.PartType)
			assert.Empty(t, body, "a rejected request must not produce an upstream body")

			cls, ok := proxy.ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, http.StatusBadRequest, cls.Status)
			assert.True(t, cls.Kind.IsClientError())
			assert.Contains(t, cls.Message, tc.partType)
			assert.Contains(t, cls.Message, tc.target)
		})
	}
}
