package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSanitizeHeadersDropsCredentialsAccountIdentifiersAndRequestCounters(t *testing.T) {
	h := http.Header{}
	for _, name := range []string{
		"Authorization", "X-Api-Key", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"Anthropic-Organization-Id", "Request-Id", "Cf-Ray",
		"Anthropic-Ratelimit-Requests-Remaining", "Anthropic-Ratelimit-Tokens-Reset",
		"Openai-Organization", "Openai-Project", "Openai-Processing-Ms", "X-Request-Id",
		"X-Ratelimit-Remaining-Tokens", "X-Ratelimit-Reset-Project-Tokens",
	} {
		h.Set(name, "secret-"+name)
	}
	h.Set("Content-Type", "text/event-stream")

	got := sanitizeHeaders(h)
	if len(got) != 1 || got["Content-Type"] != "text/event-stream" {
		t.Fatalf("sanitizeHeaders kept %v, want only Content-Type", got)
	}
}

func TestCassetteBodyIsStoredAsPlainJSONString(t *testing.T) {
	encoded, err := json.Marshal(cassette{Body: rawTextBody(`data: {"type":"ping"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"body":"data: {\"type\":\"ping\"}"`)) {
		t.Fatalf("cassette body is not a plain JSON string: %s", encoded)
	}
	var decoded cassette
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Body) != `data: {"type":"ping"}` {
		t.Fatalf("round-tripped body = %q", decoded.Body)
	}
}

func TestReplayOnlyCacheMissAnswersWithCloseDelimited502(t *testing.T) {
	s, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{cfg: config{mode: modeReplayOnly}, store: s}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	served := make(chan error, 1)
	go func() {
		served <- p.serveOne(server, "api.anthropic.com")
		_ = server.Close()
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = req.Write(client) }()
	resp, err := http.ReadResponse(bufio.NewReader(client), req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if resp.ContentLength != -1 || !resp.Close {
		t.Fatalf("content length = %d close = %v, want a close-delimited body", resp.ContentLength, resp.Close)
	}
	if !strings.Contains(string(body), "cache miss") {
		t.Fatalf("body = %q, want the cache-miss reason", body)
	}
	if err := <-served; err == nil {
		t.Fatal("serveOne reported no error for a cache miss")
	}
}

func TestRecordPersistsDecompressedSanitizedResponse(t *testing.T) {
	recordedKey := requestKey(http.MethodPost, "/v1/messages", []byte(`{}`))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	live := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("upstream request lost Accept-Encoding: %v", r.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Encoding": {"gzip"},
				"Content-Type":     {"application/json"},
				"Request-Id":       {"req_secret"},
			},
			Body: io.NopCloser(bytes.NewReader(compressed.Bytes())),
		}, nil
	})}
	s, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{cfg: config{mode: modeRecord}, store: s, live: live}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")

	if _, err := p.resolve("api.anthropic.com", req, []byte(`{}`), recordedKey); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.load(recordedKey)
	if !ok {
		t.Fatal("record mode did not persist a cassette")
	}
	if string(stored.Body) != `{"ok":true}` {
		t.Fatalf("stored body = %q, want decompressed JSON", stored.Body)
	}
	want := map[string]string{"Content-Type": "application/json"}
	if len(stored.Headers) != len(want) || stored.Headers["Content-Type"] != want["Content-Type"] {
		t.Fatalf("stored headers = %v, want %v", stored.Headers, want)
	}
}

func TestCAWritesOnlyItsPublicCertificate(t *testing.T) {
	authority, err := newCA()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := authority.writePublicCert(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != caCertFilename {
		t.Fatalf("cert dir holds %v, want only %s", entries, caCertFilename)
	}
	contents, err := os.ReadFile(filepath.Join(dir, caCertFilename))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(contents)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("cert file is not exactly one CERTIFICATE block: %q", contents)
	}
}

func TestTLSConfigServesACachedLeafPerSNIHost(t *testing.T) {
	authority, err := newCA()
	if err != nil {
		t.Fatal(err)
	}
	cfg := authority.tlsConfigFor()
	first, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.openai.com"})
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("leaf for the same host was minted twice")
	}
	if first == other {
		t.Fatal("different SNI hosts share one leaf")
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.cert)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "api.anthropic.com", Roots: roots}); err != nil {
		t.Fatalf("leaf does not verify for its SNI host against the CA: %v", err)
	}
}
