package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type proxy struct {
	cfg   config
	ca    *ca
	store *store
	live  *http.Client
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "mitmproxy: only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "mitmproxy: hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("mitmproxy: hijack failed: %v", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		log.Printf("mitmproxy: write CONNECT ack: %v", err)
		return
	}

	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	tlsConn := tls.Server(clientConn, p.ca.tlsConfigFor())
	defer func() { _ = tlsConn.Close() }()
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		log.Printf("mitmproxy: TLS handshake with client for %s: %v", strconv.Quote(host), err)
		return
	}

	if err := p.serveOne(tlsConn, host); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("mitmproxy: serve %s: %v", strconv.Quote(host), err)
	}
}

func (p *proxy) serveOne(conn net.Conn, host string) error {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return fmt.Errorf("read tunneled request: %w", err)
	}
	defer func() { _ = req.Body.Close() }()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}

	key := requestKey(req.Method, req.URL.Path, body)
	log.Printf("mitmproxy: %s key=%s mode=%s", strconv.Quote(req.Method+" "+host+req.URL.Path), key[:12], p.cfg.mode)

	c, err := p.resolve(host, req, body, key)
	if err != nil {
		writeErr := writeCassetteResponse(conn, &cassette{
			StatusCode: http.StatusBadGateway,
			Headers:    map[string]string{"Content-Type": "text/plain"},
			Body:       []byte(err.Error()),
		})
		if writeErr != nil {
			return fmt.Errorf("resolve response: %w (and failed to write error response: %v)", err, writeErr)
		}
		return fmt.Errorf("resolve response: %w", err)
	}

	return writeCassetteResponse(conn, c)
}

func (p *proxy) resolve(host string, req *http.Request, body []byte, key string) (*cassette, error) {
	switch p.cfg.mode {
	case modeReplayOnly:
		c, ok := p.store.load(key)
		if !ok {
			return nil, fmt.Errorf("%w: %s %s (key=%s) — record a cassette first (SMOKE_PROXY_MODE=record or replay-or-record)",
				errCacheMiss, req.Method, req.URL.Path, key)
		}
		return c, nil

	case modeRecord:
		return p.recordLive(host, req, body, key)

	default:
		if c, ok := p.store.load(key); ok {
			return c, nil
		}
		return p.recordLive(host, req, body, key)
	}
}

func (p *proxy) recordLive(host string, req *http.Request, body []byte, key string) (*cassette, error) {
	targetURL := &url.URL{
		Scheme:   "https",
		Host:     host,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	}
	target := &http.Request{
		Method: req.Method,
		URL:    targetURL,
		Header: req.Header.Clone(),
		Body:   io.NopCloser(bytes.NewReader(body)),
	}
	target.ContentLength = int64(len(body))

	resp, err := p.live.Do(target)
	if err != nil {
		return nil, fmt.Errorf("live upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read live response body: %w", err)
	}

	headers := sanitizeHeaders(resp.Header)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gr, gzErr := gzip.NewReader(bytes.NewReader(respBody))
		if gzErr != nil {
			return nil, fmt.Errorf("open gzip response body: %w", gzErr)
		}
		decompressed, readErr := io.ReadAll(gr)
		_ = gr.Close()
		if readErr != nil {
			return nil, fmt.Errorf("decompress gzip response body: %w", readErr)
		}
		respBody = decompressed
		delete(headers, "Content-Encoding")
	}

	c := &cassette{
		Method:     req.Method,
		Path:       req.URL.Path,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       respBody,
	}
	if err := p.store.save(key, c); err != nil {
		log.Printf("mitmproxy: WARNING failed to persist cassette %s: %v", key, err)
	} else {
		log.Printf("mitmproxy: recorded cassette key=%s status=%d bytes=%d", key[:12], resp.StatusCode, len(respBody))
	}
	return c, nil
}

func writeCassetteResponse(conn net.Conn, c *cassette) error {
	resp := &http.Response{
		StatusCode:    c.StatusCode,
		Status:        http.StatusText(c.StatusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header, len(c.Headers)),
		Body:          io.NopCloser(bytes.NewReader(c.Body)),
		ContentLength: -1,
	}
	for k, v := range c.Headers {
		resp.Header.Set(k, v)
	}
	return resp.Write(conn)
}
