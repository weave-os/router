package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRescueTrimRetainsTaskAndLastToolExchange(t *testing.T) {
	for _, test := range []struct {
		name   string
		parse  func([]byte) (*RequestEnvelope, error)
		path   string
		task   string
		call   string
		result string
	}{
		{"anthropic", ParseAnthropic, "messages", `{"role":"user","content":"Keep the actual task"}`, `{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"read","input":{}}]}`, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"latest result"}]}`},
		{"openai", ParseOpenAI, "messages", `{"role":"user","content":"Keep the actual task"}`, `{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{}"}}]}`, `{"role":"tool","tool_call_id":"call","content":"latest result"}`},
		{"gemini", ParseGemini, "contents", `{"role":"user","parts":[{"text":"Keep the actual task"}]}`, `{"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]}`, `{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"latest result"}}}]}`},
	} {
		for _, tail := range []int{12, 6, 3, 1} {
			t.Run(fmt.Sprintf("%s_tail_%d", test.name, tail), func(t *testing.T) {
				messages := []string{test.task}
				for i := 0; i < 30; i++ {
					messages = append(messages, test.call, test.result)
				}
				body := []byte(fmt.Sprintf(`{"model":"test","%s":[%s]}`, test.path, strings.Join(messages, ",")))
				env, err := test.parse(body)
				require.NoError(t, err)
				elided := env.TrimLastNMessages(tail)
				kept := gjson.GetBytes(env.body, test.path).Array()
				assert.Equal(t, 61-len(kept), elided)
				require.GreaterOrEqual(t, len(kept), 3)
				assert.JSONEq(t, test.task, kept[0].Raw)
				assert.JSONEq(t, test.call, kept[len(kept)-2].Raw)
				assert.JSONEq(t, test.result, kept[len(kept)-1].Raw)
				assert.LessOrEqual(t, len(kept), tail+2)
			})
		}
	}
}

func TestRescueTrimPreservesNewestNonTextTask(t *testing.T) {
	for _, tc := range []struct {
		name, path, task, assistant string
		parse                       func([]byte) (*RequestEnvelope, error)
	}{
		{"anthropic", "messages", `{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/synthetic.png"}}]}`, `{"role":"assistant","content":"working"}`, ParseAnthropic},
		{"openai", "messages", `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/synthetic.png"}}]}`, `{"role":"assistant","content":"working"}`, ParseOpenAI},
		{"gemini", "contents", `{"role":"user","parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://example.invalid/synthetic.png"}}]}`, `{"role":"model","parts":[{"text":"working"}]}`, ParseGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []string{tc.task}
			for i := 0; i < 20; i++ {
				messages = append(messages, tc.assistant)
			}
			body := []byte(fmt.Sprintf(`{"model":"synthetic","%s":[%s]}`, tc.path, strings.Join(messages, ",")))
			env, err := tc.parse(body)
			require.NoError(t, err)
			env.TrimLastNMessages(3)
			kept := gjson.GetBytes(env.body, tc.path).Array()
			require.Len(t, kept, 3)
			assert.JSONEq(t, tc.task, kept[0].Raw)
			assert.JSONEq(t, tc.assistant, kept[len(kept)-1].Raw)
		})
	}
}
