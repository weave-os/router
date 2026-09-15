package gemini_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/gemini"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

const blockedPromptModel = "gemini-2.5-pro"

type blockedPromptFixture struct {
	providers.Client
	response string
	stream   bool
	history  []proxy.FeedbackRequest
}

func (*blockedPromptFixture) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Provider: providers.ProviderGoogle, Model: blockedPromptModel}, nil
}
func (f *blockedPromptFixture) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	if f.stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(f.response))
	return err
}
func (f *blockedPromptFixture) CompleteFeedbackRequest(_ context.Context, req proxy.FeedbackRequest) error {
	f.history = append(f.history, req)
	return nil
}
func (*blockedPromptFixture) AcceptRouterFeedback(_ context.Context, event proxy.RouterFeedbackEvent) (proxy.RouterFeedbackEvent, error) {
	return event, nil
}

func TestPromptBlockPreservedWithFeedbackHistory(t *testing.T) {
	const blocked = `{"promptFeedback":{"blockReason":"SAFETY","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"HIGH"}]},"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`
	for _, stream := range []bool{false, true} {
		for _, emptyCandidates := range []bool{false, true} {
			for _, marker := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v/emptyCandidates=%v/marker=%v", stream, emptyCandidates, marker), func(t *testing.T) {
					response := blocked
					if emptyCandidates {
						response = `{"candidates":[],` + strings.TrimPrefix(response, "{")
					}
					action, contentType := ":generateContent", "application/json"
					if stream {
						response = "data: " + response + "\n\n"
						action, contentType = ":streamGenerateContent", "text/event-stream"
					}
					fixture := &blockedPromptFixture{response: response, stream: stream}
					svc := proxy.NewService(fixture, map[string]providers.Client{providers.ProviderGoogle: fixture}, nil, false, nil, nil, false, providers.ProviderGoogle, blockedPromptModel, nil).WithRouterFeedbackStore(fixture)
					ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, uuid.NewString())
					ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "prompt-block-test-key")
					req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+blockedPromptModel+action, strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)).WithContext(ctx)
					if !marker {
						req.Header.Set("X-Weave-Routing-Marker", "off")
					}
					rec := httptest.NewRecorder()
					engine := gin.New()
					engine.POST("/v1beta/models/:modelAction", gemini.GenerateContentHandler(svc, nil))
					engine.ServeHTTP(rec, req)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, contentType, rec.Result().Header.Get("Content-Type"))
					if stream && marker {
						prefix := `data: {"candidates":[{"content":{"parts":[{"text":"✦ **Weave Router** → gemini-2.5-pro · best pick for this turn\u000a\u000a"}],"role":"model"},"index":0}]}` + "\n\n"
						require.Equal(t, prefix+response, rec.Body.String())
					} else {
						require.Equal(t, response, rec.Body.String())
					}
					require.Empty(t, fixture.history, "a prompt block is not successful inference")
				})
			}
		}
	}
}
