package main

import (
	"testing"

	"weave-os/router/internal/postgres/pgtls"

	"github.com/stretchr/testify/assert"
)

func TestPostgresTLSConfigReadsCertificateMaterialVerbatim(t *testing.T) {
	t.Setenv("POSTGRES_SERVER_CA_CERT", " ca ")
	t.Setenv("POSTGRES_CLIENT_CERT", "cert")
	t.Setenv("POSTGRES_CLIENT_KEY", "key")
	t.Setenv("POSTGRES_SERVER_NAME", "project:region:instance")

	assert.Equal(t, pgtls.Config{ServerCACert: " ca ", ClientCert: "cert", ClientKey: "key", ServerName: "project:region:instance"}, postgresTLSConfigFromEnv())
}

func TestPostgresTLSConfigDefaultsToNoClientCertificate(t *testing.T) {
	for _, name := range []string{"POSTGRES_SERVER_CA_CERT", "POSTGRES_CLIENT_CERT", "POSTGRES_CLIENT_KEY", "POSTGRES_SERVER_NAME"} {
		t.Setenv(name, "")
	}
	assert.Equal(t, pgtls.Config{}, postgresTLSConfigFromEnv())
}
