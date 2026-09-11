package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type originalCaptureProvider struct{ body []byte }

func (p *originalCaptureProvider) Proxy(ctx context.Context, _ router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.body = append([]byte(nil), prepared.Body...)
	_, err := w.Write([]byte(`{"content":[{"type":"text","text":"unmodified history served"}]}`))
	return err
}

func (p *originalCaptureProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestDependencyFallbackRestoresHistoryAfterCompactionFailure(t *testing.T) {
	provider := &originalCaptureProvider{}
	summarizer := &fakeCompactionSummarizer{err: errors.New("summary dependency unavailable")}
	service := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithCompaction(summarizer, 0.85).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	body := toolHeavyAnthropicBody(20, 300)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx, _, finish := service.prepareOriginalRequest(context.Background(), body, recorder, request, originalMessages)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	compaction, err := service.maybeCompact(ctx, envelope, compactionInput{TurnType: turntype.MainLoop, MaxWindow: 500, Headers: http.Header{}})
	require.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)
	assert.Positive(t, compaction.ToolResultsCleared)
	assert.Equal(t, 1, summarizer.calls)
	require.NoError(t, finish(ctx, err))
	assert.Equal(t, string(body), string(provider.body))
	assert.JSONEq(t, `{"content":[{"type":"text","text":"unmodified history served"}]}`, recorder.Body.String())
	assert.Equal(t, "auxiliary_unavailable", recorder.Header().Get(HeaderRouterFailOpenReason))
}
