package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTestPlanSurfaceAllowsGeminiInferenceRoutesOnly(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "generate content", method: http.MethodPost, path: "/v1beta/models/gemini-pro:generateContent", want: true},
		{name: "stream generate content", method: http.MethodPost, path: "/v1beta/models/gemini-pro:streamGenerateContent", want: true},
		{name: "query parameters", method: http.MethodPost, path: "/v1beta/models/gemini-pro:generateContent?alt=sse", want: true},
		{name: "other Gemini method", method: http.MethodPost, path: "/v1beta/models/gemini-pro:countTokens"},
		{name: "other Gemini version", method: http.MethodPost, path: "/v1/models/gemini-pro:generateContent"},
		{name: "GET inference route", method: http.MethodGet, path: "/v1beta/models/gemini-pro:generateContent"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parsedURL, err := url.Parse(testCase.path)
			require.NoError(t, err)
			request := &http.Request{Method: testCase.method, URL: parsedURL}
			require.Equal(t, testCase.want, testPlanSurface(request))
		})
	}
}
