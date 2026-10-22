package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

func TestDirectRoutingModelInferenceSelection(t *testing.T) {
	for _, test := range []struct {
		name      string
		method    string
		path      string
		body      string
		model     router.RoutingModel
		inference bool
	}{
		{name: "implicit current classifier", method: http.MethodPost, path: "/v1/messages", body: `{}`, model: router.ModelAutomatic, inference: true},
		{name: "explicit current classifier", method: http.MethodPost, path: "/v1/responses", body: `{"model":"` + string(router.ModelAutomatic) + `"}`, model: router.ModelAutomatic, inference: true},
		{name: "new HMM", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"` + string(router.ModelHMM) + `"}`, model: router.ModelHMM, inference: true},
		{name: "Qwen", method: http.MethodPost, path: "/v1/messages", body: `{"model":"` + string(router.ModelQwen) + `"}`, model: router.ModelQwen, inference: true},
		{name: "Gemini selector", method: http.MethodPost, path: "/v1beta/models/" + string(router.ModelQwen) + ":streamGenerateContent", body: `{}`, model: router.ModelQwen, inference: true},
		{name: "Gemini URL overrides new HMM body", method: http.MethodPost, path: "/v1beta/models/" + string(router.ModelQwen) + ":generateContent", body: `{"model":"` + string(router.ModelHMM) + `"}`, model: router.ModelQwen, inference: true},
		{name: "Gemini URL overrides current HMM body", method: http.MethodPost, path: "/v1beta/models/" + string(router.ModelQwen) + ":streamGenerateContent", body: `{"model":"` + string(router.ModelAutomatic) + `"}`, model: router.ModelQwen, inference: true},
		{name: "inspection", method: http.MethodGet, path: "/v1/models", body: `{}`},
		{name: "prepare", method: http.MethodPost, path: "/v1/router/models/" + string(router.ModelQwen) + "/prepare", body: `{}`},
		{name: "unknown selector", method: http.MethodPost, path: "/v1/messages", body: `{"model":"weave-unknown"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			model, inference := directRoutingModel(request, []byte(test.body))
			assert.Equal(t, test.model, model)
			assert.Equal(t, test.inference, inference)
		})
	}
}

type routingModelAdmissionFunc func(context.Context, string, string, string, policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error)

func (f routingModelAdmissionFunc) Admit(ctx context.Context, installation, credential, session string, decide policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	return f(ctx, installation, credential, session, decide)
}

func TestGeminiRoutingModelPersistsURLClassifier(t *testing.T) {
	for _, bodyModel := range []router.RoutingModel{router.ModelAutomatic, router.ModelHMM} {
		t.Run(string(bodyModel), func(t *testing.T) {
			var stored *policyregistry.SessionReleaseBinding
			primary := routingModelAdmissionFunc(func(ctx context.Context, installation, credential, _ string, decide policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
				binding, err := decide(ctx, policyregistry.SerializedAdmission{Projection: policyregistry.AdmissionProjection{Target: policyregistry.TargetInternal}, Previous: stored})
				if err == nil {
					stored = &binding
				}
				return policyregistry.AdmissionScope{InstallationID: installation, CredentialIdentity: credential, Persistent: true}, binding, err
			})
			cache, err := policyregistry.NewAdmissionDecisionCache(primary, 10, time.Minute, time.Now)
			require.NoError(t, err)
			decide := func(context.Context, policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
				return policyregistry.SessionReleaseBinding{Target: policyregistry.TargetInternal}, nil
			}
			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+string(router.ModelQwen)+":generateContent", nil)
			model, inference := directRoutingModel(request, []byte(`{"model":"`+string(bodyModel)+`"}`))
			require.True(t, inference)
			_, binding, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", model, decide)
			require.NoError(t, err)
			assert.Equal(t, router.ModelQwen, binding.RoutingModel)
			require.NotNil(t, stored)
			assert.Equal(t, router.ModelQwen, stored.RoutingModel)
			_, _, err = cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", bodyModel, decide)
			require.ErrorIs(t, err, policyregistry.ErrRoutingModelChanged)
		})
	}
}
