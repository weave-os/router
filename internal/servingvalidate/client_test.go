package servingvalidate_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/servingvalidate"
)

func TestPrivateValidationPreservesIAMAudienceAndExactSnapshot(t *testing.T) {
	selection := policyregistry.WorkerValidationRequest{Target: policyregistry.TargetStable, ProfileKey: "opaque-profile", Selection: policyregistry.ServingSelection{Release: policyregistry.ObjectRef{SHA256: "release"}, Binding: policyregistry.ObjectRef{SHA256: "binding"}}}
	worker := policyregistry.WorkerAttestation{Ready: true, Selection: selection.Selection}
	classifier := policyregistry.ClassifierAttestation{Ready: true, Revision: "classifier-exact"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer private-identity", r.Header.Get("X-Serverless-Authorization"))
		require.Empty(t, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case policyregistry.WorkerValidationPath:
			require.Equal(t, http.MethodPost, r.Method)
			var request policyregistry.WorkerValidationRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Equal(t, selection, request)
			require.NoError(t, json.NewEncoder(w).Encode(worker))
		case policyregistry.ClassifierAttestationPath:
			require.Equal(t, http.MethodGet, r.Method)
			require.NoError(t, json.NewEncoder(w).Encode(classifier))
		default:
			t.Errorf("unexpected validation path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	audience := "https://approved-service.example"
	client, err := servingvalidate.New(server.Client(), func(_ context.Context, got string) (string, error) {
		require.Equal(t, audience, got)
		return "private-identity", nil
	})
	require.NoError(t, err)
	revision := policyregistry.RevisionBinding{URL: server.URL, Audience: audience}
	observedWorker, err := client.ValidateWorker(context.Background(), revision, selection)
	require.NoError(t, err)
	require.Equal(t, worker, observedWorker)
	observedClassifier, err := client.AttestClassifier(context.Background(), revision)
	require.NoError(t, err)
	require.Equal(t, classifier, observedClassifier)
}

func TestPrivateWorkerValidationPreservesEveryManagedTarget(t *testing.T) {
	requests := make(chan policyregistry.WorkerValidationRequest, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request policyregistry.WorkerValidationRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests <- request
		require.NoError(t, json.NewEncoder(w).Encode(policyregistry.WorkerAttestation{Ready: true, Selection: request.Selection}))
	}))
	defer server.Close()
	client, err := servingvalidate.New(server.Client(), func(context.Context, string) (string, error) {
		return "private-identity", nil
	})
	require.NoError(t, err)
	for _, target := range []policyregistry.ServingTarget{
		policyregistry.TargetStaging,
		policyregistry.TargetStable,
		policyregistry.TargetInternal,
	} {
		request := policyregistry.WorkerValidationRequest{
			Target: target,
			Selection: policyregistry.ServingSelection{
				Release: policyregistry.ObjectRef{SHA256: "release-" + string(target)},
				Binding: policyregistry.ObjectRef{SHA256: "binding-" + string(target)},
			},
		}
		_, err := client.ValidateWorker(context.Background(), policyregistry.RevisionBinding{URL: server.URL, Audience: server.URL}, request)
		require.NoError(t, err)
		require.Equal(t, request, <-requests)
	}
}

func TestPrivateValidationRejectsRedirectsAndIncompleteWireResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		payload  string
		expected string
	}{
		{"redirect", http.StatusTemporaryRedirect, "", "HTTP 307"},
		{"unauthorized internal ingress", http.StatusForbidden, "private secret", "HTTP 403"},
		{"legacy endpoint", http.StatusNotFound, "", "serving attestation support"},
		{"trailing JSON", http.StatusOK, `{} {}`, "trailing JSON"},
		{"unknown fields", http.StatusOK, `{"untrusted":true}`, "unknown field"},
		{"oversized", http.StatusOK, strings.Repeat(" ", (1<<20)+1), "size limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != policyregistry.ClassifierAttestationPath {
					t.Error("followed redirect")
				}
				w.Header().Set("Location", "/stolen-token")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			client, err := servingvalidate.New(server.Client(), func(context.Context, string) (string, error) { return "private-identity", nil })
			require.NoError(t, err)
			_, err = client.AttestClassifier(context.Background(), policyregistry.RevisionBinding{URL: server.URL, Audience: server.URL})
			require.ErrorContains(t, err, test.expected)
			require.NotContains(t, err.Error(), "private secret")
		})
	}
}

func TestPrivateValidationRejectsNonHTTPSRevisionsBeforeMintingTokens(t *testing.T) {
	client, err := servingvalidate.New(&http.Client{}, func(context.Context, string) (string, error) {
		t.Error("token minted for a plaintext or credentialed destination")
		return "", nil
	})
	require.NoError(t, err)
	for _, revision := range []policyregistry.RevisionBinding{
		{URL: "http://localhost", Audience: "https://service.example"},
		{URL: "https://service.example", Audience: "http://service.example"},
		{URL: "https://user:password@service.example", Audience: "https://service.example"},
		{URL: "", Audience: "https://service.example"},
	} {
		_, err := client.AttestClassifier(context.Background(), revision)
		require.ErrorContains(t, err, "HTTPS")
	}
	_, err = servingvalidate.New(nil, func(context.Context, string) (string, error) { return "", nil })
	require.Error(t, err)
	_, err = servingvalidate.New(&http.Client{}, nil)
	require.Error(t, err)
}

func TestPrivateValidationAllowsScaleFromZeroBeforeEndpointWork(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		require.True(t, ok)
		require.GreaterOrEqual(t, time.Until(deadline), 90*time.Second)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"revision":"classifier-exact","ready":true}`)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	client, err := servingvalidate.New(httpClient, func(context.Context, string) (string, error) {
		return "private-identity", nil
	})
	require.NoError(t, err)

	attestation, err := client.AttestClassifier(context.Background(), policyregistry.RevisionBinding{
		URL:      "https://classifier.example",
		Audience: "https://classifier.example",
	})
	require.NoError(t, err)
	require.Equal(t, "classifier-exact", attestation.Revision)
	require.True(t, attestation.Ready)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
