package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/server"
)

func TestLocalInspectionTrustIsScopedAndVerifiesTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	certificateFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600))
	original := http.DefaultTransport
	transport, err := localInspectionTransport(server.DeploymentModeSelfHosted, "prod/stable", certificateFile)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	systemRoots, err := x509.SystemCertPool()
	require.NoError(t, err)
	require.NotNil(t, systemRoots)
	transportRootSubjects := make(map[string]struct{})
	for _, subject := range transport.TLSClientConfig.RootCAs.Subjects() {
		transportRootSubjects[string(subject)] = struct{}{}
	}
	for _, subject := range systemRoots.Subjects() {
		_, exists := transportRootSubjects[string(subject)]
		require.True(t, exists, "local inspection trust must retain system root %q", subject)
	}
	response, err := (&http.Client{Transport: transport}).Get(upstream.URL)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Same(t, original, http.DefaultTransport, "constructing local trust must not mutate global clients")
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
	_, err = localInspectionTransport(server.DeploymentModeManaged, "prod/stable", certificateFile)
	require.Error(t, err)
	_, err = localInspectionTransport(server.DeploymentModeSelfHosted, "", certificateFile)
	require.Error(t, err)
}

func TestLocalInspectionProviderTransportTrust(t *testing.T) {
	if certificateFile := os.Getenv("ROUTER_TEST_INSPECTION_CERT"); certificateFile != "" {
		require.NoError(t, configureLocalInspection(server.DeploymentModeSelfHosted, "prod/stable", certificateFile))
		transport := httputil.NewTransport(time.Second, time.Second)
		defer transport.CloseIdleConnections()
		require.NotNil(t, transport.TLSClientConfig)
		require.NotNil(t, transport.TLSClientConfig.RootCAs)
		response, err := (&http.Client{Transport: transport}).Get(os.Getenv("ROUTER_TEST_INSPECTION_URL"))
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusNoContent, response.StatusCode)
		return
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	certificateFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestLocalInspectionProviderTransportTrust$")
	child.Env = append(os.Environ(), "ROUTER_TEST_INSPECTION_CERT="+certificateFile, "ROUTER_TEST_INSPECTION_URL="+upstream.URL)
	output, err := child.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
