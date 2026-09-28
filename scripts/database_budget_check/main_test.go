package main

import "testing"

func TestLoopbackDatabaseHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if !loopbackDatabaseHost(host) {
			t.Errorf("loopback host %q rejected", host)
		}
	}
	for _, host := range []string{"postgres", "db.internal", "example.com"} {
		if loopbackDatabaseHost(host) {
			t.Errorf("non-loopback host %q accepted", host)
		}
	}
}
