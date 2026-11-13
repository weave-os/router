package pgtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfigureWithoutCertificatesLeavesPgxTLSIntact(t *testing.T) {
	poolConfig := parseConfig(t, "postgres://user:pw@10.55.0.3:5432/db?sslmode=prefer")
	fallbackCount := len(poolConfig.ConnConfig.Fallbacks)

	configured, err := Configure(poolConfig)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if configured {
		t.Fatal("expected no client TLS without certificate env vars")
	}
	if len(poolConfig.ConnConfig.Fallbacks) != fallbackCount {
		t.Fatalf("fallbacks changed: got %d want %d", len(poolConfig.ConnConfig.Fallbacks), fallbackCount)
	}
}

func TestConfigureRejectsPartialCertificateMaterial(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	t.Setenv(clientCertEnvVar, certPEM)
	t.Setenv(clientKeyEnvVar, keyPEM)

	if _, err := Configure(parseConfig(t, "postgres://user:pw@10.55.0.3:5432/db")); err == nil {
		t.Fatal("expected an error when the server CA is missing")
	}
}

func TestConfigureInstallsClientCertificateAndDropsPlaintextFallback(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	t.Setenv(serverCACertEnvVar, certPEM)
	t.Setenv(clientCertEnvVar, certPEM)
	t.Setenv(clientKeyEnvVar, keyPEM)

	// sslmode=prefer is the shape that gives pgx a plaintext fallback to prune.
	poolConfig := parseConfig(t, "postgres://user:pw@10.55.0.3:5432/db?sslmode=prefer")
	configured, err := Configure(poolConfig)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !configured {
		t.Fatal("expected client TLS to be configured")
	}

	tlsConfig := poolConfig.ConnConfig.TLSConfig
	if tlsConfig == nil {
		t.Fatal("expected a TLS config on the connection")
	}
	if len(tlsConfig.Certificates) != 1 {
		t.Fatalf("expected one client certificate, got %d", len(tlsConfig.Certificates))
	}
	for _, fallback := range poolConfig.ConnConfig.Fallbacks {
		if fallback.TLSConfig != tlsConfig {
			t.Fatal("expected every remaining fallback to reuse the certificate TLS config")
		}
	}
}

func TestConfigureVerifiesTheExpectedServerIdentity(t *testing.T) {
	serverDER, certPEM, keyPEM := selfSigned(t, "workweave-staging-01:us-central1:main-instance")
	t.Setenv(serverCACertEnvVar, certPEM)
	t.Setenv(clientCertEnvVar, certPEM)
	t.Setenv(clientKeyEnvVar, keyPEM)

	for _, testCase := range []struct {
		name         string
		serverName   string
		wantAccepted bool
	}{
		{name: "unset accepts any certificate from the CA", serverName: "", wantAccepted: true},
		{name: "matching identity is accepted", serverName: "workweave-staging-01:us-central1:main-instance", wantAccepted: true},
		{name: "another endpoint under the same CA is rejected", serverName: "workweave-staging-01:us-central1:other-instance"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(serverNameEnvVar, testCase.serverName)
			poolConfig := parseConfig(t, "postgres://user:pw@10.55.0.3:5432/db")
			if _, err := Configure(poolConfig); err != nil {
				t.Fatalf("Configure: %v", err)
			}

			err := poolConfig.ConnConfig.TLSConfig.VerifyPeerCertificate([][]byte{serverDER}, nil)
			if accepted := err == nil; accepted != testCase.wantAccepted {
				t.Fatalf("accepted = %t, want %t (err: %v)", accepted, testCase.wantAccepted, err)
			}
		})
	}
}

func parseConfig(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return poolConfig
}

func selfSignedPEM(t *testing.T) (certPEM string, keyPEM string) {
	t.Helper()
	_, certPEM, keyPEM = selfSigned(t, "router-test")
	return certPEM, keyPEM
}

func selfSigned(t *testing.T, commonName string) (der []byte, certPEM string, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err = x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return der,
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}
