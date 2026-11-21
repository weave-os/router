package pgtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ClientCertEnvVar   = "POSTGRES_CLIENT_CERT"
	ClientKeyEnvVar    = "POSTGRES_CLIENT_KEY"
	ServerCACertEnvVar = "POSTGRES_SERVER_CA_CERT"
	ServerNameEnvVar   = "POSTGRES_SERVER_NAME"
)

type Config struct {
	ServerCACert string
	ClientCert   string
	ClientKey    string
	ServerName   string
}

func Configure(poolConfig *pgxpool.Config, cfg Config) (bool, error) {
	serverCACert := cfg.ServerCACert
	clientCert := cfg.ClientCert
	clientKey := cfg.ClientKey
	if serverCACert == "" && clientCert == "" && clientKey == "" {
		return false, nil
	}
	if serverCACert == "" || clientCert == "" || clientKey == "" {
		return false, fmt.Errorf("postgres client TLS needs %s, %s and %s together", ServerCACertEnvVar, ClientCertEnvVar, ClientKeyEnvVar)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(serverCACert)) {
		return false, fmt.Errorf("%s contains no usable certificate", ServerCACertEnvVar)
	}
	keyPair, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	if err != nil {
		return false, fmt.Errorf("load %s/%s key pair: %w", ClientCertEnvVar, ClientKeyEnvVar, err)
	}

	tlsConfig := &tls.Config{
		RootCAs:            roots,
		Certificates:       []tls.Certificate{keyPair},
		InsecureSkipVerify: true, // codeql[go/disabled-certificate-check]
		VerifyConnection:   verifyAgainst(roots, cfg.ServerName),
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
			ServerNameEnvVar,
			expectedServerName,
		)
	}
}
