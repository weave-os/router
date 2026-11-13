package taskdomain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/taskdomain"
)

func digest(payload []byte) string { sum := sha256.Sum256(payload); return hex.EncodeToString(sum[:]) }

func TestReleaseRejectsContractOrInventoryDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskdomain.Release)
		valid  bool
	}{
		{"valid", func(*taskdomain.Release) {}, true},
		{"prompt", func(r *taskdomain.Release) { r.PromptSHA256 = strings.Repeat("b", 64) }, false},
		{"projection", func(r *taskdomain.Release) { r.ProjectionVersion = "other" }, false},
		{"missing tokenizer", func(r *taskdomain.Release) { delete(r.Files, "tokenizer.json") }, false},
		{"traversal", func(r *taskdomain.Release) { r.Files["../model"] = strings.Repeat("b", 64) }, false},
		{"bad evidence", func(r *taskdomain.Release) { r.Evidence["roster"] = "not-a-digest" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := taskdomain.Release{SchemaVersion: taskdomain.SchemaVersion, ProjectionVersion: taskdomain.ProjectionVersion, PromptSHA256: digest([]byte(taskdomain.SystemPrompt)), Files: map[string]string{}, Evidence: map[string]string{strings.Repeat("c", 64): strings.Repeat("d", 64)}}
			for _, name := range []string{"model.safetensors", "config.json", "tokenizer.json", "tokenizer_config.json", "generation_config.json"} {
				release.Files[name] = strings.Repeat("a", 64)
			}
			tc.mutate(&release)
			payload, err := json.Marshal(release)
			require.NoError(t, err)
			parsed, err := taskdomain.ParseRelease(payload, digest(payload))
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, release, parsed)
			} else {
				require.Error(t, err)
			}
			_, err = taskdomain.ParseRelease(append(payload, ' '), digest(payload))
			require.Error(t, err)
		})
	}
}
