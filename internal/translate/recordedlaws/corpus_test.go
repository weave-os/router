package recordedlaws_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	cassetteDir  = "../../../smoke/mitmproxy/cassettes"
	syntheticDir = "testdata/synthetic"
	clientModel  = "client-model"
)

type origin string

const (
	originReal      origin = "real"
	originSynthetic origin = "synthetic"
)

type recording struct {
	name   string
	origin origin
	format wireFormat
	body   []byte
}

type cassette struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

func (c cassette) header(name string) string {
	for k, v := range c.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func classifyCassette(c cassette) (wireFormat, bool) {
	if c.Method == "HEAD" {
		return "", false
	}
	stream := strings.Contains(c.header("Content-Type"), "text/event-stream")
	switch {
	case c.Method == "POST" && c.Path == "/v1/messages" && c.StatusCode == 200 && stream:
		return anthropicSSE, true
	case c.Method == "POST" && c.Path == "/v1/messages" && c.StatusCode == 200:
		return anthropicJSON, true
	case c.Method == "POST" && c.Path == "/v1/responses" && c.StatusCode == 200 && stream:
		return responsesSSE, true
	}
	return "", true
}

func loadCassettes(t *testing.T) []recording {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(cassetteDir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no cassettes under %s", cassetteDir)
	sort.Strings(paths)
	var out []recording
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		require.NoError(t, err)
		var c cassette
		require.NoError(t, json.Unmarshal(raw, &c), p)
		format, relevant := classifyCassette(c)
		if !relevant {
			continue
		}
		require.NotEmpty(t, format, "cassette %s (%s %s %d) has no wire format in the recorded-traffic harness; add one", filepath.Base(p), c.Method, c.Path, c.StatusCode)
		out = append(out, recording{name: strings.TrimSuffix(filepath.Base(p), ".json")[:12], origin: originReal, format: format, body: []byte(c.Body)})
	}
	return out
}

func loadSynthetic(t *testing.T) []recording {
	t.Helper()
	var out []recording
	dirs, err := os.ReadDir(syntheticDir)
	require.NoError(t, err)
	for _, d := range dirs {
		format := wireFormat(d.Name())
		require.Contains(t, responseFormats(), format, "synthetic directory %s is not a response wire format", d.Name())
		files, err := os.ReadDir(filepath.Join(syntheticDir, d.Name()))
		require.NoError(t, err)
		for _, f := range files {
			body, err := os.ReadFile(filepath.Join(syntheticDir, d.Name(), f.Name()))
			require.NoError(t, err)
			out = append(out, recording{name: "synthetic_" + strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())), origin: originSynthetic, format: format, body: body})
		}
	}
	return out
}

func terminalResponsesProjection(rec recording) recording {
	return recording{name: rec.name + "_terminal", origin: rec.origin, format: responsesJSON, body: assembleResponsesSSE(rec.body)}
}

func responseCorpus(t *testing.T) []recording {
	t.Helper()
	corpus := append(loadCassettes(t), loadSynthetic(t)...)
	var projected []recording
	for _, rec := range corpus {
		if rec.format == responsesSSE {
			projected = append(projected, terminalResponsesProjection(rec))
		}
	}
	return append(corpus, projected...)
}

func realRecordings(t *testing.T, format wireFormat) []recording {
	t.Helper()
	var out []recording
	for _, rec := range loadCassettes(t) {
		if rec.format == format {
			out = append(out, rec)
		}
	}
	require.NotEmpty(t, out, "no real %s recording in the cassettes", format)
	return out
}

func realToolUseRecording(t *testing.T) recording {
	t.Helper()
	for _, rec := range realRecordings(t, anthropicSSE) {
		if gjson.GetBytes(assembleAnthropicSSE(rec.body), `content.#(type=="tool_use")`).Exists() {
			return rec
		}
	}
	t.Fatal("no real Anthropic tool_use stream in the cassettes")
	return recording{}
}

func richestResponsesEcho(t *testing.T) gjson.Result {
	t.Helper()
	var best gjson.Result
	for _, rec := range realRecordings(t, responsesSSE) {
		resp := gjson.ParseBytes(assembleResponsesSSE(rec.body))
		if len(resp.Get("tools").Array()) > len(best.Get("tools").Array()) {
			best = resp
		}
	}
	require.NotEmpty(t, best.Get("tools").Array(), "no Responses cassette echoes request tools")
	require.NotEmpty(t, best.Get("instructions").String(), "no Responses cassette echoes instructions")
	return best
}

func TestCorpusCoversEveryRecordedKind(t *testing.T) {
	counts := map[wireFormat]int{}
	for _, rec := range loadCassettes(t) {
		counts[rec.format]++
		require.Empty(t, validate(rec.format, rec.body), "real %s recording %s fails its own shape validator", rec.format, rec.name)
	}
	for _, f := range []wireFormat{anthropicJSON, anthropicSSE, responsesSSE} {
		require.Positive(t, counts[f], "cassettes lost every %s recording", f)
	}
	for _, rec := range loadSynthetic(t) {
		require.Empty(t, validate(rec.format, rec.body), "synthetic %s fixture %s fails its shape validator", rec.format, rec.name)
	}
	for _, tool := range richestResponsesEcho(t).Get("tools").Array() {
		require.Empty(t, validateResponsesTool(tool), "OpenAI-accepted tool %s fails the Responses tool validator", tool.Get("name").String())
	}
}
