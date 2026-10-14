package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"weave-os/router/internal/server"
)

// The inspector CA belongs to this process only. In particular, macOS Go does
// not honor SSL_CERT_FILE when using its native system certificate pool.
func localInspectionTransport(mode server.DeploymentMode, target, certificateFile string) (*http.Transport, error) {
	if mode != server.DeploymentModeSelfHosted || target == "" {
		return nil, errors.New("inspection trust requires a pinned selfhosted local session")
	}
	pem, err := os.ReadFile(certificateFile)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system root certificates: %w", err)
	}
	if roots == nil {
		return nil, errors.New("system root certificates are unavailable")
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("inspection CA file contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return transport, nil
}

func configureLocalInspection(mode server.DeploymentMode, target, certificateFile string) error {
	transport, err := localInspectionTransport(mode, target, certificateFile)
	if err != nil {
		return err
	}
	// Provider adapters copy this TLS configuration into their dedicated transports.
	http.DefaultTransport = transport
	return nil
}
