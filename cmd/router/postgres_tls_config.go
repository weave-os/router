package main

import (
	"os"

	"weave-os/router/internal/postgres/pgtls"
)

func postgresTLSConfigFromEnv() pgtls.Config {
	return pgtls.Config{
		ServerCACert: os.Getenv(pgtls.ServerCACertEnvVar),
		ClientCert:   os.Getenv(pgtls.ClientCertEnvVar),
		ClientKey:    os.Getenv(pgtls.ClientKeyEnvVar),
		ServerName:   os.Getenv(pgtls.ServerNameEnvVar),
	}
}
