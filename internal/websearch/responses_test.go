package websearch_test

import (
	"strings"
	"testing"

	"weave-os/router/internal/websearch"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesRequestPreservesSearchRestrictions(t *testing.T) {
	body := []byte(`{"model":"caller","system":"You are an assistant for performing a web search tool use","messages":[{"role":"user","content":"Perform a web search for the query: a public query"}],"tools":[{"type":"web_search_20250305","name":"web_search","blocked_domains":["blocked.example"],"max_uses":1,"user_location":{"type":"approximate","country":"GB"}}]}`)
	request, err := websearch.ResponsesRequest(body, "selected")
	require.NoError(t, err)
	assert.Equal(t, "selected", gjson.GetBytes(request, "model").String())
	assert.Equal(t, "blocked.example", gjson.GetBytes(request, "tools.0.filters.blocked_domains.0").String())
	assert.Equal(t, "GB", gjson.GetBytes(request, "tools.0.user_location.country").String())
	assert.False(t, gjson.GetBytes(request, "max_tool_calls").Exists())
	assert.Equal(t, 1, websearch.CallLimit(body))
	assert.Equal(t, "Perform a web search for the query: a public query", gjson.GetBytes(request, "input.0.content.0.text").String())
	assert.False(t, gjson.GetBytes(request, "store").Bool())
	assert.Equal(t, "low", gjson.GetBytes(request, "reasoning.effort").String())
	_, err = websearch.ResponsesRequest([]byte(`{"messages":[{"role":"user","content":"ordinary conversation"}]}`), "selected")
	require.Error(t, err)
}

const searchItem = `{"type":"web_search_call","status":"completed","action":{"type":"search","sources":[{"type":"url","url":"https://docs.example/guide"}]}}`
const answerItem = `{"type":"message","content":[{"type":"output_text","text":"Found the guide.","annotations":[{"type":"url_citation","url":"https://docs.example/guide","title":"Guide"}]}]}`
const usageJSON = `{"input_tokens":120,"output_tokens":22,"input_tokens_details":{"cached_tokens":40}}`

func TestParseResponsesPreservesSourcesAndMeasuredUsage(t *testing.T) {
	stream := "data: " + `{"type":"response.output_item.done","output_index":0,"item":` + searchItem + "}\n\n" +
		"data: " + `{"type":"response.output_item.done","output_index":1,"item":` + answerItem + "}\n\n" +
		"data: " + `{"type":"response.completed","response":{"status":"completed","output":[],"usage":` + usageJSON + "}}\n\n"
	response, err := websearch.ParseResponses([]byte(stream), nil)
	require.NoError(t, err)
	require.Len(t, response.Results, 1)
	assert.Equal(t, "Guide", response.Results[0].Title)
	assert.Equal(t, "https://docs.example/guide", response.Results[0].URL)
	assert.Equal(t, "Found the guide.", response.Summary)
	require.NotNil(t, response.Usage)
	assert.Equal(t, 80, response.Usage.InputTokens)
	assert.Equal(t, 40, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 22, response.Usage.OutputTokens)
	assert.Equal(t, 1, response.Usage.ServerToolUse.WebSearchRequests)
}

func TestParseResponsesRequiresSuccessfulSearchAndTerminal(t *testing.T) {
	for _, stream := range []string{
		"",
		"data: " + `{"type":"response.output_item.done","output_index":0,"item":` + searchItem + "}\n\n",
		"data: " + `{"type":"response.failed"}` + "\n\n",
		"data: " + `{"type":"response.incomplete"}` + "\n\n",
		"data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + answerItem + `],"usage":` + usageJSON + "}}\n\n",
		"data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + searchItem + "," + answerItem + "]}}\n\n",
	} {
		_, err := websearch.ParseResponses([]byte(stream), nil)
		require.Error(t, err)
	}
	valid := "data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + searchItem + "," + answerItem + `],"usage":` + usageJSON + "}}\n\n"
	response, err := websearch.ParseResponses([]byte(strings.ReplaceAll(valid, "\n", "\r\n")), nil)
	require.NoError(t, err)
	assert.Equal(t, "Found the guide.", response.Summary)
}

func TestParseResponsesEnforcesDomainRestrictions(t *testing.T) {
	stream := "data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + searchItem + "," + answerItem + `],"usage":` + usageJSON + "}}\n\n"
	for _, request := range []string{`{"tools":[{"blocked_domains":["docs.example"]}]}`, `{"tools":[{"allowed_domains":["other.example"]}]}`} {
		_, err := websearch.ParseResponses([]byte(stream), []byte(request))
		require.ErrorContains(t, err, "domain restrictions")
	}
	_, err := websearch.ParseResponses([]byte(stream), []byte(`{"tools":[{"allowed_domains":["example"]}]}`))
	require.NoError(t, err)
}

func TestParseResponsesCountsSearchActionsAndKeepsFailureDetail(t *testing.T) {
	open := `{"type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://docs.example/guide"}}`
	stream := "data: " + `{"type":"response.completed","response":{"model":"served-model","status":"completed","output":[` + searchItem + "," + open + "," + answerItem + `],"usage":` + usageJSON + "}}\n\n"
	response, err := websearch.ParseResponses([]byte(stream), []byte(`{"tools":[{"max_uses":2}]}`))
	require.NoError(t, err)
	assert.Equal(t, 1, response.Usage.ServerToolUse.WebSearchRequests)
	assert.Equal(t, "served-model", response.Model)
	_, err = websearch.ParseResponses([]byte("data: "+`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"search capacity is busy"}}}`+"\n\n"), nil)
	require.ErrorContains(t, err, "rate_limit_exceeded")
	assert.ErrorContains(t, err, "search capacity is busy")
}

func TestResponsesRequestRejectsUnsupportedRestrictionsBeforeSearch(t *testing.T) {
	base := `{"model":"caller","system":"You are an assistant for performing a web search tool use","messages":[{"role":"user","content":"Perform a web search for the query: a public query"}],"tools":[{"type":"web_search_20250305","name":"web_search",FIELD}]}`
	for _, field := range []string{`"allowed_domains":["example.com/docs"]`, `"blocked_domains":["example.com/private"]`, `"blocked_domains":["*.example.com"]`, `"allowed_domains":"example.com"`, `"max_uses":0`, `"blocked_domains":["example.com:443"]`, `"allowed_domains":[" example.com"]`, `"allowed_domains":["."]`, `"allowed_domains":[".example.com"]`, `"blocked_domains":["a..b"]`, `"blocked_domains":["blocked.example.."]`, `"max_uses":11`} {
		_, err := websearch.ResponsesRequest([]byte(strings.Replace(base, "FIELD", field, 1)), "selected")
		require.Error(t, err)
	}
}

func TestParseResponsesChecksOpenedPagesAndFlatErrors(t *testing.T) {
	open := `{"type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://blocked.example/private"}}`
	stream := "data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + searchItem + "," + open + "," + answerItem + `],"usage":` + usageJSON + "}}\n\n"
	_, err := websearch.ParseResponses([]byte(stream), []byte(`{"tools":[{"blocked_domains":["blocked.example"],"max_uses":2}]}`))
	require.ErrorContains(t, err, "domain restrictions")
	_, err = websearch.ParseResponses([]byte("data: "+`{"type":"error","code":"rate_limit_exceeded","message":"search is busy"}`+"\n\n"), nil)
	require.ErrorContains(t, err, "rate_limit_exceeded")
	assert.ErrorContains(t, err, "search is busy")
}

func TestParseResponsesRejectsMalformedRestrictedURLs(t *testing.T) {
	bad := `{"type":"web_search_call","status":"completed","action":{"type":"open_page","url":"javascript:bad"}}`
	stream := "data: " + `{"type":"response.completed","response":{"status":"completed","output":[` + searchItem + "," + bad + "," + answerItem + `],"usage":` + usageJSON + "}}\n\n"
	_, err := websearch.ParseResponses([]byte(stream), []byte(`{"tools":[{"allowed_domains":["docs.example"],"max_uses":2}]}`))
	require.ErrorContains(t, err, "domain restrictions")
}

func TestNativeSearchOperationBudget(t *testing.T) {
	base := `{"model":"caller","system":"You are an assistant for performing a web search tool use","messages":[{"role":"user","content":"Perform a web search for the query: a public query"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":10}]}`
	_, err := websearch.ResponsesRequest([]byte(base), "selected")
	require.NoError(t, err)
	assert.Equal(t, 10, websearch.CallLimit([]byte(base)))
	assert.Equal(t, 10, websearch.CallLimit([]byte(strings.Replace(base, `"max_uses":10`, `"max_uses":1000000`, 1))))
}
