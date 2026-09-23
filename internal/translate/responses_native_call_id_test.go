package translate_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClampResponsesInputCallIDs_ClampsSignatureCarryingIDs(t *testing.T) {
	sig := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 160)))
	longID := "call_abc123__thought__" + sig
	require.Greater(t, len(longID), 64)

	body := []byte(`{"model":"gpt-6-astra","input":[` +
		`{"type":"message","role":"user","content":"hi"},` +
		`{"type":"function_call","id":"fc_1","call_id":"` + longID + `","name":"shell","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + longID + `","output":"ok"},` +
		`{"type":"function_call","id":"fc_2","call_id":"call_short","name":"shell","arguments":"{}"}` +
		`],"stream":true}`)

	out, err := translate.ClampResponsesInputCallIDs(body)
	require.NoError(t, err)

	call := gjson.GetBytes(out, "input.1.call_id").Str
	output := gjson.GetBytes(out, "input.2.call_id").Str
	assert.LessOrEqual(t, len(call), 64)
	assert.NotContains(t, call, "__thought__")
	assert.Equal(t, call, output, "function_call and function_call_output must stay correlated")
	assert.Equal(t, "call_short", gjson.GetBytes(out, "input.3.call_id").Str)
	assert.Equal(t, "fc_1", gjson.GetBytes(out, "input.1.id").Str)
	assert.Equal(t, "gpt-6-astra", gjson.GetBytes(out, "model").Str)
	assert.True(t, gjson.GetBytes(out, "stream").Bool())
}

func TestClampResponsesInputCallIDs_NoChangeWithoutLongIDs(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"type":"function_call","call_id":"call_1","name":"x","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"y"}]}`)
	out, err := translate.ClampResponsesInputCallIDs(body)
	require.NoError(t, err)
	assert.Equal(t, string(body), string(out))

	noInput := []byte(`{"model":"gpt-6-astra","input":"plain prompt"}`)
	out, err = translate.ClampResponsesInputCallIDs(noInput)
	require.NoError(t, err)
	assert.Equal(t, string(noInput), string(out))
}
