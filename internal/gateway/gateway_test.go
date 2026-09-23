package gateway_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/translate"
)

const registryRoot = "gs://test-bucket/registry"

type credentialVerifier struct{ failure error }

func (v credentialVerifier) VerifyRoutingCredential(context.Context, string) (*auth.Installation, *auth.APIKey, error) {
	return &auth.Installation{ID: "installation"}, &auth.APIKey{ID: "key"}, v.failure
}

type admissionStore struct {
	admission        policyregistry.SessionReleaseBinding
	failure          error
	seenConversation string
}

func (s *admissionStore) Admit(_ context.Context, installation, key, conversation string, _ policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	s.seenConversation = conversation
	digest, persistent := policyregistry.ServingConversationDigest(key, conversation)
	return policyregistry.AdmissionScope{InstallationID: installation, CredentialIdentity: key, ConversationDigest: digest, Persistent: persistent}, s.admission, s.failure
}

type bindingStore struct {
	policyregistry.ServingStore
	binding policyregistry.DeploymentBinding
}

func (s bindingStore) RootURI() string { return registryRoot }
func (s bindingStore) selectionSet() policyregistry.SelectionSet {
	payload, _ := policyregistry.CanonicalBytes(s.binding)
	return policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: s.binding.Target, Default: policyregistry.ServingSelection{Release: s.binding.Release, Binding: servingRef(policyregistry.ServingBindings, payload)}, Profiles: map[string]policyregistry.ServingSelection{}}
}
func (s bindingStore) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, _ policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	var manifest policyregistry.ServingManifest = &s.binding
	if kind == policyregistry.ServingSelectionSets {
		set := s.selectionSet()
		manifest = &set
	}
	payload, err := policyregistry.CanonicalBytes(manifest)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}
func (s bindingStore) ReadServingState(_ context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	if target != s.binding.Target {
		return policyregistry.ServingStateSnapshot{}, errors.New("wrong target")
	}
	payload, _ := policyregistry.CanonicalBytes(s.selectionSet())
	id := uuid.NewString()
	activation := policyregistry.Activation{ID: id, Sequence: 1, SelectionSet: servingRef(policyregistry.ServingSelectionSets, payload), ActivatedAt: time.Now().Add(-time.Hour), Proposal: servingRef(policyregistry.ServingProposals, []byte("proposal")), RequestID: uuid.NewString(), Actor: "operator", WorkflowActor: "workflow"}
	return policyregistry.ServingStateSnapshot{Generation: 1, State: policyregistry.ServingControlState{SchemaVersion: policyregistry.ServingControlStateV1, Target: target, CurrentActivationID: id, Sequence: 1, Activations: map[string]policyregistry.Activation{id: activation}}}, nil
}

type revisionAuthorizer struct{}

func (revisionAuthorizer) IdentityToken(context.Context, string) (string, error) {
	return "gateway-iam", nil
}

func artifact(label string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{URI: registryRoot + "/artifacts/" + label, SHA256: policyregistry.Digest([]byte(label)), Generation: 1}
}
func servingRef(kind policyregistry.ServingKind, payload []byte) policyregistry.ObjectRef {
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: registryRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
}

func gatewayBinding(workerURL string) policyregistry.DeploymentBinding {
	release := servingRef(policyregistry.ServingReleases, []byte("release"))
	revision := policyregistry.RevisionBinding{Name: "worker-0001", URL: workerURL, Audience: "https://worker.example", ImageDigest: "sha256:" + strings.Repeat("a", 64), Configuration: artifact("configuration")}
	return policyregistry.DeploymentBinding{SchemaVersion: policyregistry.ServingBindingV1, Target: policyregistry.TargetStable, Project: "project", Region: "region", Release: release, Router: revision, Classifier: revision, ClassifierBundleSHA256: strings.Repeat("b", 64), Attestation: artifact("attestation")}
}

func gatewayFixture(t *testing.T, worker *httptest.Server, authFailure, admissionFailure error, products ...gateway.ProductSurfaces) (*gateway.Handler, *admissionStore, *policyregistry.AssertionSigner) {
	t.Helper()
	binding := gatewayBinding(worker.URL)
	payload, err := policyregistry.CanonicalBytes(binding)
	require.NoError(t, err)
	admissions := &admissionStore{admission: policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "activation", BindingGeneration: 1, Selection: policyregistry.ServingSelection{Release: binding.Release, Binding: servingRef(policyregistry.ServingBindings, payload)}}, failure: admissionFailure}
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	forwarder, err := gateway.NewHandler(credentialVerifier{authFailure}, admissions, bindingStore{binding: binding}, signer, revisionAuthorizer{}, worker.Client().Transport, products...)
	require.NoError(t, err)
	return forwarder, admissions, signer
}

func TestGatewayPreservesBytesCredentialsAndStripsSpoofedAssertions(t *testing.T) {
	const body = "{ \"model\": \"auto\",\n \"stream\":true, \"metadata\":{\"user_id\":\"{\\\"session_id\\\":\\\"body-session\\\"}\"} }"
	var calls atomic.Int32
	type observedRequest struct {
		body    []byte
		headers http.Header
		uri     string
	}
	observed := make(chan observedRequest, 1)
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		payload, _ := io.ReadAll(r.Body)
		observed <- observedRequest{payload, r.Header.Clone(), r.URL.RequestURI()}
		w.Header().Set("X-Weave-Serving-Assertion", "do-not-leak")
		w.Header().Set("X-Worker-Status", "preserved")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("worker error bytes"))
	}))
	defer worker.Close()
	forwarder, admissions, signer := gatewayFixture(t, worker, nil, nil)
	r := httptest.NewRequest(http.MethodPost, "http://gateway/v1/messages?original=1", strings.NewReader(body))
	r.Header.Set(auth.RouterKeyHeader, "rk_credential")
	r.Header.Set("Authorization", "Bearer subscription")
	r.Header.Set("x-api-key", "provider-key")
	r.Header.Set("Session-Id", "header-session")
	r.Header.Set(policyregistry.ServingAssertionHeader, "spoofed")
	r.Header.Set("X-Weave-Serving-Target", "prod/weave-internal")
	r.Header.Set("X-Weave-Internal-Subject", "spoofed")
	r.Header.Set(policyregistry.ServerlessAuthorizationHeader, "Bearer client-iam")
	w := httptest.NewRecorder()
	forwarder.ServeHTTP(w, r)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "worker error bytes", w.Body.String())
	assert.Empty(t, w.Header().Get(policyregistry.ServingAssertionHeader))
	assert.Equal(t, "preserved", w.Header().Get("X-Worker-Status"))
	require.Equal(t, int32(1), calls.Load())
	seen := <-observed
	assert.Equal(t, body, string(seen.body))
	assert.Equal(t, "/v1/messages?original=1", seen.uri)
	assert.Equal(t, "body-session", admissions.seenConversation)
	assert.Equal(t, "Bearer subscription", seen.headers.Get("Authorization"))
	assert.Equal(t, "provider-key", seen.headers.Get("x-api-key"))
	assert.Equal(t, "Bearer gateway-iam", seen.headers.Get(policyregistry.ServerlessAuthorizationHeader))
	assert.Empty(t, seen.headers.Get("X-Weave-Serving-Target"))
	assert.Empty(t, seen.headers.Get("X-Weave-Internal-Subject"))
	verified, err := signer.Verify(seen.headers.Get(policyregistry.ServingAssertionHeader), r, seen.body, "rk_credential")
	require.NoError(t, err)
	assert.Equal(t, policyregistry.TargetStable, verified.Admission.Target)
}

func TestGatewayStreamsBeforeCompletionAndPropagatesCancellation(t *testing.T) {
	cancelled := make(chan struct{})
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(t, worker, nil, nil)
	entry := httptest.NewServer(forwarder)
	defer entry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL+"/v1/responses", strings.NewReader(`{"input":"hello","stream":true}`))
	require.NoError(t, err)
	r.Header.Set(auth.RouterKeyHeader, "rk_credential")
	response, err := entry.Client().Do(r)
	require.NoError(t, err)
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: first\n", line)
	cancel()
	response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not observe cancellation")
	}
}

func TestGatewayDoesNotDispatchOnAdmissionFailure(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer worker.Close()
	for _, test := range []struct {
		name                  string
		authErr, admissionErr error
		status                int
	}{
		{"revoked credential", auth.ErrInvalidToken, nil, http.StatusUnauthorized},
		{"pending subject", nil, auth.ErrPersonalCredentialRequired, http.StatusUnauthorized},
		{"authoritative state unavailable", nil, errors.New("registry unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			forwarder, _, _ := gatewayFixture(t, worker, test.authErr, test.admissionErr)
			w := httptest.NewRecorder()
			forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
			assert.Equal(t, test.status, w.Code)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestGatewayRetiresBetaWithoutSessionOrWorkerAdmission(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer worker.Close()
	for _, test := range []struct{ name, path, body, expected string }{
		{"anthropic json", "/v1/messages", `{"messages":[{"role":"user","content":"/beta"}]}`, `"type":"message"`},
		{"anthropic stream", "/v1/messages", `{"stream":true,"messages":[{"role":"user","content":"/beta"}]}`, "event: message_stop"},
		{"chat json", "/v1/chat/completions", `{"messages":[{"role":"user","content":"/beta"}]}`, `"object":"chat.completion"`},
		{"chat stream", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"/beta"}]}`, "data: [DONE]"},
		{"responses json", "/v1/responses", `{"input":"/beta"}`, `"object":"response"`},
		{"responses stream", "/v1/responses", `{"stream":true,"input":"/beta"}`, "response.completed"},
		{"gemini json", "/v1beta/models/auto:generateContent", `{"contents":[{"role":"user","parts":[{"text":"/beta"}]}]}`, `"finishReason":"STOP"`},
		{"gemini stream", "/v1beta/models/auto:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"/beta"}]}]}`, "data: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			forwarder, _, _ := gatewayFixture(t, worker, nil, errors.New("admission must not run for retired beta"))
			w := httptest.NewRecorder()
			forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), translate.BetaRetiredMessage)
			assert.Contains(t, w.Body.String(), test.expected)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestGatewayDoesNotFollowWorkerRedirects(t *testing.T) {
	var redirected atomic.Int32
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer untrusted.Close()
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, untrusted.URL, http.StatusTemporaryRedirect)
	}))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(t, worker, nil, nil)
	w := httptest.NewRecorder()
	forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Zero(t, redirected.Load())
}
