package llmescalation_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/llmescalation"
)

func TestRecordServedReleaseAttributesVerdictToServingRelease(t *testing.T) {
	job := llmescalation.Job{Provider: llmescalation.QwenProvider, RendererRevision: llmescalation.QwenRendererVersion}
	job.RecordServedRelease(nil)
	require.Empty(t, job.Model)

	job.RecordServedRelease(&llmescalation.ServedRelease{Name: "qwen-escalation-v1.1.0", ModelSHA256: "model-digest", PromptSHA256: "prompt-digest"})
	require.Equal(t, "qwen-escalation-v1.1.0", job.Model)
	require.Equal(t, "model-digest", job.ModelSHA256)
	require.Equal(t, "prompt-digest", job.PromptRevision)
	require.Equal(t, llmescalation.QwenRendererVersion, job.RendererRevision)
}
