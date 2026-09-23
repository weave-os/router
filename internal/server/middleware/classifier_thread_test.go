package middleware_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/classifier"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
)

type threadEndpointStore struct{ threads []router.ClassifierThread }

func (s *threadEndpointStore) Create(_ context.Context, thread router.ClassifierThread) (router.ClassifierThread, error) {
	for _, saved := range s.threads {
		if saved.RequestID == thread.RequestID && saved.InstallationID == thread.InstallationID && saved.CredentialSHA256 == thread.CredentialSHA256 {
			return saved, nil
		}
	}
	s.threads = append(s.threads, thread)
	return thread, nil
}

func (*threadEndpointStore) WithThread(context.Context, router.ClassifierThread, func(router.ClassifierTurnStore) error) error {
	panic("HTTP admission must not classify")
}

type unusedThreadClassifier struct{}

func (unusedThreadClassifier) Classify(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
	panic("HTTP admission must not classify")
}

func TestClassifierThreadHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	installation := uuid.New()
	store := &threadEndpointStore{}
	svc := &proxy.Service{}
	require.NoError(t, svc.WithClassifierSessions(proxy.ClassifierSessionConfig{Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64), SelectionPolicySHA256: strings.Repeat("b", 64), SigningKey: []byte(strings.Repeat("k", 32)), InstallationIDs: []uuid.UUID{installation}}, store, unusedThreadClassifier{}))
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installation.String())
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "test-credential")
	ctx = router.WithStrategy(ctx, router.StrategyHMMEmbedding)
	engine := gin.New()
	engine.POST("/v1/router/threads", classifier.StartThreadHandler(svc))
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1beta/models/:modelAction", "/v1/route/handoff", "/v1/route", "/v1/route/preview"} {
		engine.POST(path, middleware.WithClassifierThread(svc), func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Empty(t, c.GetHeader(proxy.ClassifierThreadHeader))
			require.NotContains(t, string(body), "weave_classifier_thread")
			c.JSON(http.StatusOK, gin.H{"strategy": router.StrategyFromContext(c.Request.Context()), "body": json.RawMessage(body)})
		})
	}
	request := func(principal context.Context, path, body, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(principal)
		if header != "" {
			req.Header.Set(proxy.ClassifierThreadHeader, header)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	start := `{"new_chat_id":"` + uuid.NewString() + `"}`
	created := request(ctx, "/v1/router/threads", start, "")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Equal(t, "no-store", created.Header().Get("Cache-Control"))
	var ticket struct {
		Token string `json:"thread_token"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &ticket))
	require.NotEmpty(t, ticket.Token)
	require.Equal(t, created.Body.String(), request(ctx, "/v1/router/threads", start, "").Body.String())
	require.Len(t, store.threads, 1)
	for _, body := range []string{`{}`, `{"new_chat_id":"bad"}`, start + ` {}`, strings.TrimSuffix(start, "}") + `,"extra":true}`, strings.Repeat(" ", 1025) + start} {
		require.Equal(t, http.StatusBadRequest, request(ctx, "/v1/router/threads", body, "").Code)
	}
	foreign := context.WithValue(ctx, proxy.InstallationIDContextKey{}, uuid.NewString())
	require.Equal(t, http.StatusForbidden, request(foreign, "/v1/router/threads", start, "").Code)
	require.Equal(t, http.StatusForbidden, request(context.Background(), "/v1/router/threads", start, "").Code)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1beta/models/test:generateContent"} {
		t.Run(path, func(t *testing.T) {
			denied := request(ctx, path, `{}`, proxy.ClassifierThreadUnavailableToken)
			require.Equal(t, http.StatusBadRequest, denied.Code)
			require.Contains(t, denied.Body.String(), "classifier_client_unavailable")
			baseline := request(ctx, path, `{"model":"test","messages":[]}`, "")
			require.Equal(t, http.StatusOK, baseline.Code)
			require.Contains(t, baseline.Body.String(), `"strategy":"hmm_embedding"`)
			for _, header := range []string{"", ticket.Token} {
				body := `{"model":"test","weave_classifier_thread":"` + ticket.Token + `","messages":[]}`
				admitted := request(ctx, path, body, header)
				require.Equal(t, http.StatusOK, admitted.Code, admitted.Body.String())
				require.Contains(t, admitted.Body.String(), `"strategy":"llm_classifier"`)
				require.NotContains(t, admitted.Body.String(), ticket.Token)
			}
			require.Equal(t, http.StatusOK, request(ctx, path, `{}`, ticket.Token).Code)
			for _, body := range []string{`{"weave_classifier_thread":null}`, `{"weave_classifier_thread":17}`, `{"weave_classifier_thread":""}`, `{"weave_classifier_thread":"forged"}`} {
				require.Equal(t, http.StatusConflict, request(ctx, path, body, "").Code)
			}
			require.Equal(t, http.StatusConflict, request(ctx, path, `{"weave_classifier_thread":"forged"}`, ticket.Token).Code)
			require.Equal(t, http.StatusConflict, request(foreign, path, `{}`, ticket.Token).Code)
			otherKey := context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "other-credential")
			require.Equal(t, http.StatusConflict, request(otherKey, path, `{}`, ticket.Token).Code)
		})
	}
	for _, path := range []string{"/v1/route/handoff", "/v1/route", "/v1/route/preview"} {
		require.Equal(t, http.StatusConflict, request(ctx, path, `{}`, ticket.Token).Code)
	}
}
