package pgtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	clientCertEnvVar   = "POSTGRES_CLIENT_CERT"
	clientKeyEnvVar    = "POSTGRES_CLIENT_KEY"
	serverCACertEnvVar = "POSTGRES_SERVER_CA_CERT"
	serverNameEnvVar   = "POSTGRES_SERVER_NAME"
)

func Configure(poolConfig *pgxpool.Config) (bool, error) {
	serverCACert := os.Getenv(serverCACertEnvVar)
	clientCert := os.Getenv(clientCertEnvVar)
	clientKey := os.Getenv(clientKeyEnvVar)
	if serverCACert == "" && clientCert == "" && clientKey == "" {
		return false, nil
	}
	if serverCACert == "" || clientCert == "" || clientKey == "" {
		return false, fmt.Errorf("postgres client TLS needs %s, %s and %s together", serverCACertEnvVar, clientCertEnvVar, clientKeyEnvVar)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(serverCACert)) {
		return false, fmt.Errorf("%s contains no usable certificate", serverCACertEnvVar)
	}
	keyPair, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	if err != nil {
		return false, fmt.Errorf("load %s/%s key pair: %w", clientCertEnvVar, clientKeyEnvVar, err)
	}

	tlsConfig := &tls.Config{
		RootCAs:            roots,
		Certificates:       []tls.Certificate{keyPair},
		InsecureSkipVerify: true, // codeql[go/disabled-certificate-check]
		VerifyConnection:   verifyAgainst(roots, os.Getenv(serverNameEnvVar)),
	}
	poolConfig.ConnConfig.TLSConfig = tlsConfig

	var encryptedFallbacks []*pgconn.FallbackConfig
	for _, fallback := range poolConfig.ConnConfig.Fallbacks {
		if fallback.TLSConfig == nil {
			continue
		}
		fallback.TLSConfig = tlsConfig
		encryptedFallbacks = append(encryptedFallbacks, fallback)
	}
	poolConfig.ConnConfig.Fallbacks = encryptedFallbacks

	return true, nil
}

func verifyAgainst(roots *x509.CertPool, expectedServerName string) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("postgres server presented no certificate")
		}
		serverCert := state.PeerCertificates[0]
		if _, err := serverCert.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
			return err
		}
		if expectedServerName == "" {
			return nil
		}
		if serverCert.Subject.CommonName == expectedServerName ||
			slices.Contains(serverCert.DNSNames, expectedServerName) {
			return nil
		}
		return fmt.Errorf(
			"postgres server certificate identifies %q, not the %s %q",
			serverCert.Subject.CommonName,
			serverNameEnvVar,
			expectedServerName,
		)
	}
}
