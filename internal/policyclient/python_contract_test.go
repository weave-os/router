package policyclient

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// CI supplies the actual bundled sidecar environment; ordinary Go-only builds
// can run without Python. HMM_CONTRACT_EXPORT also exports these synthetic
// wire fixtures for the separately owned managed raw-V5 contract gate.
func TestBundledPythonAcceptsGoClassificationEvidence(t *testing.T) {
	python := os.Getenv("HMM_CONTRACT_PYTHON")
	if python == "" {
		t.Skip("set HMM_CONTRACT_PYTHON to the bundled sidecar Python")
	}
	var requests []json.RawMessage
	for position := 0; position < 97; position++ {
		messages := make([]router.ConversationMessage, 97)
		for i := range messages {
			messages[i] = router.ConversationMessage{Role: "assistant", Text: strings.Repeat("context", 500)}
		}
		messages[position] = router.ConversationMessage{Role: "user", Text: "Inspect the synthetic regression"}
		body, err := marshalRouteRequest(policy.Query{Strategy: router.StrategyHMM, SchemaVersion: policy.SchemaVersionV3, ConversationMessages: messages})
		require.NoError(t, err)
		requests = append(requests, body)
	}
	wire, err := json.Marshal(requests)
	require.NoError(t, err)
	if path := os.Getenv("HMM_CONTRACT_EXPORT"); path != "" {
		require.NoError(t, os.WriteFile(path, wire, 0600))
	}
	program := `import json, sys
from hmm_sidecar.features import conversation_sequence
payloads = json.load(sys.stdin)
for payload in payloads:
    users = [turn.text for turn in conversation_sequence(payload) if turn.kind == "user"]
    assert users and "Inspect the synthetic regression" in users[-1], users
    assert "[missing prompt text]" not in users[-1]
print(len(payloads))`
	command := exec.Command(python, "-c", program)
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join("..", "..", "sidecars", "hmm"))
	command.Stdin = bytes.NewReader(wire)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Equal(t, "97", strings.TrimSpace(string(output)))
}
