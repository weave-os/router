package classifier_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/classifier"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type handlerPreparationRouter struct {
	state policy.PreparationState
	calls int
}

func (p *handlerPreparationRouter) Route(context.Context, router.Request) (router.Decision, error) {
	panic("preparation must not perform inference")
}

func (p *handlerPreparationRouter) PrepareClassifier(context.Context, string) (policy.ClassifierPreparation, error) {
	p.calls++
	return policy.ClassifierPreparation{ModelID: "qwen-artifact", BundleSHA256: strings.Repeat("b", 64), ServingRevision: strings.Repeat("c", 64), State: p.state, OperationID: "fc-synthetic1"}, nil
}

func TestPrepareModelHandlerEnforcesRequestAndPreparationContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		method    string
		body      string
		operation string
		state     policy.PreparationState
		target    policyregistry.ServingTarget
		status    int
		calls     int
	}{
		{"start", http.MethodPost, `{}`, "", policy.PreparationWarming, policyregistry.TargetInternal, http.StatusAccepted, 1},
		{"ready", http.MethodGet, "", strings.Repeat("a", 64) + ".hmm_qwen.fc-synthetic1", policy.PreparationReady, policyregistry.TargetInternal, http.StatusOK, 1},
		{"caller endpoint", http.MethodPost, `{"endpoint":"https://untrusted.example"}`, "", policy.PreparationReady, policyregistry.TargetInternal, http.StatusBadRequest, 0},
		{"null", http.MethodPost, `null`, "", policy.PreparationReady, policyregistry.TargetInternal, http.StatusBadRequest, 0},
		{"trailing body", http.MethodPost, `{} {}`, "", policy.PreparationReady, policyregistry.TargetInternal, http.StatusBadRequest, 0},
		{"wrong release", http.MethodGet, "", strings.Repeat("e", 64) + ".hmm_qwen.fc-synthetic1", policy.PreparationReady, policyregistry.TargetInternal, http.StatusConflict, 0},
		{"stable", http.MethodPost, `{}`, "", policy.PreparationReady, policyregistry.TargetStable, http.StatusForbidden, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preparer := &handlerPreparationRouter{state: tc.state}
			snapshot := &policyregistry.Snapshot{Candidate: policyregistry.Candidate{
				HeadSnapshot: policyregistry.HeadSnapshot{Head: policyregistry.LaneHead{ReleaseSHA256: strings.Repeat("a", 64)}},
				Release: policyregistry.Release{Classifier: policyregistry.ClassifierIdentity{Variants: map[router.Strategy]policy.ClassifierArtifact{
					router.StrategyHMMQwen: {ArtifactID: "qwen-artifact", PackageSHA256: strings.Repeat("b", 64), ServingRevision: strings.Repeat("c", 64)},
				}}},
			}, Routers: map[router.Strategy]router.Router{router.StrategyHMMQwen: preparer}}
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{
					Target: string(tc.target), ReleaseID: strings.Repeat("a", 64), BindingID: strings.Repeat("d", 64), CredentialIdentity: "synthetic-subject",
				})
				c.Request = c.Request.WithContext(policyregistry.WithServingSnapshot(ctx, snapshot))
			})
			engine.POST("/v1/router/models/:model/prepare", classifier.PrepareModelHandler(&proxy.Service{}))
			engine.GET("/v1/router/models/:model/prepare/:operation", classifier.PrepareModelHandler(&proxy.Service{}))
			path := "/v1/router/models/" + string(router.ModelQwen) + "/prepare"
			if tc.operation != "" {
				path += "/" + tc.operation
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(tc.method, path, strings.NewReader(tc.body)))
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.Equal(t, tc.calls, preparer.calls)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			if tc.status == http.StatusAccepted || tc.status == http.StatusOK {
				var preparation proxy.ModelPreparation
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &preparation))
				require.Equal(t, strings.Repeat("a", 64)+".hmm_qwen.fc-synthetic1", preparation.OperationID)
				require.Equal(t, tc.state, preparation.State)
			}
			if tc.status == http.StatusAccepted {
				require.Equal(t, "2", response.Header().Get("Retry-After"))
			}
		})
	}
}
