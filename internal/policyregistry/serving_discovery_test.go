package policyregistry_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestDiscoverySelectionPreservesExactDefaultReferences(t *testing.T) {
	legacySelectionSet := fixtureSet("discovery")
	selectionSet := fixtureSetV2("discovery")
	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetStaging} {
		for name, selection := range map[string]policyregistry.ServingSelection{
			"legacy":        legacySelectionSet.Default,
			"selection set": selectionSet.View(artifactStoredRef([]byte("discovery-set"))).Default,
		} {
			t.Run(string(target)+"/"+name, func(t *testing.T) {
				request := policyregistry.WorkerValidationRequest{Target: target, Selection: selection}
				encoded, err := policyregistry.EncodeDiscoverySelection(request)
				require.NoError(t, err)
				decoded, err := policyregistry.DecodeDiscoverySelection(encoded)
				require.NoError(t, err)
				assert.Equal(t, target, decoded.Target)
				assert.Equal(t, selection.Release, decoded.Selection.Release)
				assert.Equal(t, selection.Binding, decoded.Selection.Binding)
				assert.Empty(t, decoded.ProfileKey)
				assert.Nil(t, decoded.Selection.Profile)
			})
		}
	}
}

func TestDiscoverySelectionRejectsPrivateAndIncompleteSelections(t *testing.T) {
	for name, mutate := range map[string]func(*policyregistry.WorkerValidationRequest){
		"internal target": func(request *policyregistry.WorkerValidationRequest) { request.Target = policyregistry.TargetInternal },
		"unknown target":  func(request *policyregistry.WorkerValidationRequest) { request.Target = "prod/beta" },
		"profile key":     func(request *policyregistry.WorkerValidationRequest) { request.ProfileKey = profileKeyOne },
		"profile revision": func(request *policyregistry.WorkerValidationRequest) {
			request.Selection.Profile = &request.Selection.Binding
		},
		"missing release": func(request *policyregistry.WorkerValidationRequest) {
			request.Selection.Release = policyregistry.ObjectRef{}
		},
		"missing binding": func(request *policyregistry.WorkerValidationRequest) {
			request.Selection.Binding = policyregistry.ObjectRef{}
		},
		"missing generation": func(request *policyregistry.WorkerValidationRequest) { request.Selection.Release.Generation = 0 },
		"invalid digest":     func(request *policyregistry.WorkerValidationRequest) { request.Selection.Binding.SHA256 = "invalid" },
		"non-registry uri": func(request *policyregistry.WorkerValidationRequest) {
			request.Selection.Binding.URI = "https://worker.example"
		},
		"oversized metadata": func(request *policyregistry.WorkerValidationRequest) {
			request.Selection.Release.URI = "gs://fixture/" + strings.Repeat("x", 16*1024)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := policyregistry.WorkerValidationRequest{Target: policyregistry.TargetStable, Selection: fixtureSet("discovery").Default}
			mutate(&request)
			encoded, err := policyregistry.EncodeDiscoverySelection(request)
			require.Error(t, err)
			assert.Empty(t, encoded)
			payload, err := policyregistry.CanonicalBytes(request)
			require.NoError(t, err)
			decoded, err := policyregistry.DecodeDiscoverySelection(base64.RawURLEncoding.EncodeToString(payload))
			require.Error(t, err)
			assert.Equal(t, policyregistry.WorkerValidationRequest{}, decoded)
		})
	}
}

func TestDiscoverySelectionRejectsMalformedMetadata(t *testing.T) {
	request := policyregistry.WorkerValidationRequest{Target: policyregistry.TargetStable, Selection: fixtureSet("discovery").Default}
	payload, err := policyregistry.CanonicalBytes(request)
	require.NoError(t, err)
	encode := func(payload string) string { return base64.RawURLEncoding.EncodeToString([]byte(payload)) }
	for name, encoded := range map[string]string{
		"absent":               "",
		"bad base64":           "not!base64",
		"oversized":            strings.Repeat("a", 16*1024+1),
		"bad json":             encode("{"),
		"null":                 encode("null"),
		"unknown field":        encode(strings.TrimSuffix(string(payload), "}") + `,"unknown":true}`),
		"unknown nested field": encode(strings.Replace(string(payload), `"release":{`, `"release":{"unknown":true,`, 1)),
		"trailing object":      encode(string(payload) + "{}"),
		"trailing garbage":     encode(string(payload) + "invalid"),
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := policyregistry.DecodeDiscoverySelection(encoded)
			require.Error(t, err)
			assert.Equal(t, policyregistry.WorkerValidationRequest{}, decoded)
		})
	}
}
