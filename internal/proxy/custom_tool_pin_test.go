package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

// A session pinned to a model that cannot fill projected custom tools must not
// survive a custom-tool turn just because its context window fits.
func TestService_SessionPin_CustomToolTurnKeepsUnsupportedPinExcluded(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderDeepInfra,
		Model:         catalog.ModelMiMoV26Pro,
		Reason:        "cluster:v0.57",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	fresh := catalog.ModelIDGPT55.String()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: fresh, Reason: "cluster:v0.57"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderOpenAI: &fakeProvider{}, providers.ProviderDeepInfra: &fakeProvider{}},
		nil,
		store,
		false,
		providers.ProviderOpenAI,
		catalog.ModelIDGPT55.String(),
		nil,
	)

	body := `{"model":"auto","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic task"}]}],"tools":[{"type":"custom","name":"exec"}]}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(authedCtx(uuid.New().String()), []byte(body), rec, httpReq))

	require.NotNil(t, fr.capturedReq, "scorer must run once the unsupported pin is dropped")
	assert.Contains(t, fr.capturedReq.ExcludedModels, catalog.ModelMiMoV26Pro)
	assert.Equal(t, fresh, rec.Header().Get(proxy.HeaderRouterModel))
}
