package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	cfg := configFromEnv()

	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck(cfg.listen))
	}

	ca, err := newCA()
	if err != nil {
		log.Fatalf("mitmproxy: mint CA: %v", err)
	}
	if err := ca.writePublicCert(cfg.certDir); err != nil {
		log.Fatalf("mitmproxy: write CA cert: %v", err)
	}
	log.Printf("mitmproxy: CA public cert at %s/%s", cfg.certDir, caCertFilename)

	store, err := newStore(cfg.cacheDir)
	if err != nil {
		log.Fatalf("mitmproxy: open cassette store: %v", err)
	}

	p := &proxy{cfg: cfg, ca: ca, store: store, live: liveClient()}

	srv := &http.Server{
		Addr:         cfg.listen,
		Handler:      http.HandlerFunc(p.handle),
		ReadTimeout:  0,
		WriteTimeout: 0,
	}
	log.Printf("mitmproxy: listening on %s mode=%s cache=%s", cfg.listen, cfg.mode, cfg.cacheDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("mitmproxy: serve: %v", err)
	}
}

type config struct {
	listen   string
	certDir  string
	cacheDir string
	mode     string
}

const (
	modeReplayOnly     = "replay-only"
	modeRecord         = "record"
	modeReplayOrRecord = "replay-or-record"
)

func configFromEnv() config {
	cfg := config{
		listen:   envOr("SMOKE_PROXY_LISTEN", ":8888"),
		certDir:  envOr("SMOKE_PROXY_CERT_DIR", "certs"),
		cacheDir: envOr("SMOKE_PROXY_CACHE_DIR", "cassettes"),
		mode:     envOr("SMOKE_PROXY_MODE", modeReplayOrRecord),
	}
	switch cfg.mode {
	case modeReplayOnly, modeRecord, modeReplayOrRecord:
	default:
		fmt.Fprintf(os.Stderr, "mitmproxy: invalid SMOKE_PROXY_MODE %q\n", cfg.mode)
		os.Exit(2)
	}
	if err := os.MkdirAll(cfg.certDir, 0o755); err != nil {
		log.Fatalf("mitmproxy: mkdir cert dir: %v", err)
	}
	if err := os.MkdirAll(cfg.cacheDir, 0o755); err != nil {
		log.Fatalf("mitmproxy: mkdir cache dir: %v", err)
	}
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func liveClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second}
}

func healthcheck(listen string) int {
	addr := listen
	if addr == "" || addr[0] == ':' {
		addr = "localhost" + addr
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mitmproxy: healthcheck dial %s: %v\n", addr, err)
		return 1
	}
	_ = conn.Close()
	return 0
}
