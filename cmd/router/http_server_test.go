package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestHTTPServer(t *testing.T, handler http.Handler) (*http.Server, string) {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := newHTTPServer(listener.Addr().String(), handler)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return srv, "http://" + listener.Addr().String()
}

// h2cClient speaks HTTP/2 with prior knowledge over cleartext, as Cloud Run's
// frontend does when the container port is declared h2c.
func h2cClient() *http.Client {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: protocols}}
}

func http1Client() *http.Client {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Client{Transport: &http.Transport{Protocols: protocols}}
}

func sendTestRequest(ctx context.Context, client *http.Client, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func flush(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Error("response writer does not support flushing")
		return
	}
	flusher.Flush()
}

func TestHTTPServerServesH2CAndHTTP1OnSamePort(t *testing.T) {
	_, base := startTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.Proto)
	}))
	for name, tc := range map[string]struct {
		client *http.Client
		proto  string
	}{
		"h2c prior knowledge": {client: h2cClient(), proto: "HTTP/2.0"},
		"http1":               {client: http1Client(), proto: "HTTP/1.1"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := sendTestRequest(t.Context(), tc.client, http.MethodGet, base+"/livez", nil)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.proto, resp.Proto)
			assert.Equal(t, tc.proto, string(body))
		})
	}
}

func TestHTTPServerH2CReceivesLargeRequestBody(t *testing.T) {
	const size = 48 << 20
	_, base := startTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, n)
	}))
	resp, err := sendTestRequest(t.Context(), h2cClient(), http.MethodPost, base+"/v1/responses", bytes.NewReader(make([]byte, size)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 2, resp.ProtoMajor)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, fmt.Sprint(size), string(body))
}

func TestHTTPServerH2CFlushesStreamedEventsIncrementally(t *testing.T) {
	release := make(chan struct{})
	_, base := startTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: first\n\n")
		flush(t, w)
		<-release
		_, _ = fmt.Fprint(w, "data: second\n\n")
	}))
	resp, err := sendTestRequest(t.Context(), h2cClient(), http.MethodGet, base+"/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 2, resp.ProtoMajor)
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err, "the first event must arrive before the handler finishes")
	assert.Equal(t, "data: first\n", line)
	close(release)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "\ndata: second\n\n", string(rest))
}

func TestHTTPServerH2CClientCancelCancelsRequestContext(t *testing.T) {
	canceled := make(chan struct{})
	started := make(chan struct{})
	_, base := startTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flush(t, w)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	resp, err := sendTestRequest(ctx, h2cClient(), http.MethodGet, base+"/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 2, resp.ProtoMajor)
	<-started
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler context was not canceled after the client reset the stream")
	}
}

func TestHTTPServerH2CShutdownDrainsInFlightStream(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv, base := startTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = fmt.Fprint(w, "done")
	}))
	type result struct {
		body string
		err  error
	}
	results := make(chan result, 1)
	go func() {
		resp, err := sendTestRequest(t.Context(), h2cClient(), http.MethodGet, base+"/v1/responses", nil)
		if err != nil {
			results <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		results <- result{body: string(body), err: err}
	}()
	<-started
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned %v before the in-flight stream finished", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	got := <-results
	require.NoError(t, got.err)
	assert.Equal(t, "done", got.body)
	require.NoError(t, <-shutdownDone)
	resp, err := sendTestRequest(t.Context(), h2cClient(), http.MethodGet, base+"/livez", nil)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "a drained server must not accept new connections")
}
