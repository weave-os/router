// Package pgtls configures Postgres mutual TLS from PEM material in the environment.
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

// Configure installs a client certificate and server CA from POSTGRES_CLIENT_CERT,
// POSTGRES_CLIENT_KEY and POSTGRES_SERVER_CA_CERT onto every connection the pool opens,
// and reports whether it did. Deployments without those variables keep pgx's own TLS
// handling, which is how self-hosters connect.
//
// Required by Cloud SQL instances running with ssl_mode
// TRUSTED_CLIENT_CERTIFICATE_REQUIRED, which reject a private-IP connection that presents
// no client certificate.
//
// POSTGRES_SERVER_NAME, when set, is the identity the server certificate must carry, e.g.
// the Cloud SQL instance connection name. Set it whenever the server CA signs for more than
// one database endpoint, since chain validation alone would then accept any of them.
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
		RootCAs:      roots,
		Certificates: []tls.Certificate{keyPair},
		// Cloud SQL server certificates carry the instance connection name rather than the
		// IP being dialed, so Go's default hostname check can never pass. VerifyPeerCertificate
		// below replaces it with a chain check against the configured CA — the connection is
		// still authenticated, just not by hostname.
		InsecureSkipVerify:    true, // codeql[go/disabled-certificate-check]
		VerifyPeerCertificate: verifyAgainst(roots, os.Getenv(serverNameEnvVar)),
	}
	poolConfig.ConnConfig.TLSConfig = tlsConfig

	// pgx's sslmode fallbacks would otherwise retry with their own TLS settings, or with no
	// encryption at all, silently discarding the certificate the server demands.
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

func verifyAgainst(roots *x509.CertPool, expectedServerName string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("postgres server presented no certificate")
		}
		serverCert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		if _, err = serverCert.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
			return err
		}
		if expectedServerName == "" {
			return nil
		}
		// Cloud SQL puts the instance connection name in the subject rather than a DNS SAN,
		// so both carry an identity worth matching.
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
