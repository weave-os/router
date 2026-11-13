package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/trafficcapture"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTrafficCaptureWriterRollsBackFailedRecord(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	capture, err := newTrafficCaptureFromEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, capture.Close()) })
	require.NoError(t, capture.Record(trafficcapture.Exchange{ID: "before"}))

	spool := &trafficcapture.BodySpool{}
	spool.Write([]byte("synthetic body"))
	require.NoError(t, spool.Close())
	require.Error(t, capture.Record(trafficcapture.Exchange{
		ID: "failed", Request: trafficcapture.Request{BodySpool: spool},
	}))
	require.NoError(t, capture.Record(trafficcapture.Exchange{ID: "after"}))

	capturedBytes, err := os.ReadFile(capturePath)
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSpace(capturedBytes), []byte("\n"))
	require.Len(t, lines, 2)
	for index, expectedID := range []string{"before", "after"} {
		var exchange trafficcapture.Exchange
		require.NoError(t, json.Unmarshal(lines[index], &exchange))
		require.Equal(t, expectedID, exchange.ID)
	}
}

func TestTrafficCaptureCorrelatesActualLocalHTTPExchanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Errorf("provider received authorization %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: upstream\n\n")
	}))
	defer provider.Close()

	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	t.Setenv(trafficCaptureSensitiveHeadersEnv, "false")
	capture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(middleware.WithTrafficCapture(capture))
	providerClient := httputil.NewClient(nil)
	engine.POST("/v1/messages", func(c *gin.Context) {
		upstreamRequest, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, provider.URL+"/v1/messages", c.Request.Body)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		upstreamRequest.Header.Set("Authorization", "Bearer provider-secret")
		response, err := providerClient.Do(upstreamRequest)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		c.Status(response.StatusCode)
		_, _ = io.Copy(c.Writer, response.Body)
	})
	router := httptest.NewServer(engine)
	defer router.Close()

	request, err := http.NewRequest(http.MethodPost, router.URL+"/v1/messages", strings.NewReader("synthetic prompt"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer harness-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(responseBody) != "data: upstream\n\n" {
		t.Fatalf("router response = %q, err = %v", responseBody, err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	fileBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var inbound, upstream trafficcapture.Exchange
	for _, line := range strings.Split(strings.TrimSpace(string(fileBytes)), "\n") {
		var exchange trafficcapture.Exchange
		if err := json.Unmarshal([]byte(line), &exchange); err != nil {
			t.Fatal(err)
		}
		if exchange.Direction == trafficcapture.DirectionInbound {
			inbound = exchange
		} else {
			upstream = exchange
		}
	}
	if inbound.ID == "" || upstream.ParentID != inbound.ID || upstream.Attempt != 1 || !inbound.Complete || !upstream.Complete {
		t.Fatalf("uncorrelated HTTP exchanges: inbound=%#v upstream=%#v", inbound, upstream)
	}
	if string(inbound.Request.Body) != "synthetic prompt" || string(upstream.Request.Body) != "synthetic prompt" ||
		string(inbound.Response.Body) != string(responseBody) || string(upstream.Response.Body) != string(responseBody) {
		t.Fatalf("HTTP bodies differ across boundaries: inbound=%#v upstream=%#v", inbound, upstream)
	}
	if strings.Contains(string(fileBytes), "harness-secret") || strings.Contains(string(fileBytes), "provider-secret") {
		t.Fatal("capture leaked authorization headers")
	}
}

func TestTrafficCaptureWriterRedactsHeadersAndPreservesBodies(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	t.Setenv(trafficCaptureSensitiveHeadersEnv, "false")
	capture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()

	exchange := trafficcapture.Exchange{
		SchemaVersion: 1,
		ID:            "inbound-1",
		Direction:     trafficcapture.DirectionInbound,
		StartedAt:     time.Now(),
		Request: trafficcapture.Request{
			Header: map[string][]string{
				"Authorization":      {"Bearer secret"},
				"X-Client-Secret":    {"opaque credential"},
				"X-Weave-User-Email": {"agent@example.com"},
				"Content-Type":       {"application/json"},
			},
			Body: []byte(`{"prompt":"synthetic"}`),
		},
		Response: &trafficcapture.Response{
			Header: map[string][]string{
				"Content-Type":    {"application/json"},
				"X-Session-Id":    {"private session"},
				"X-Client-Secret": {"opaque response credential"},
			},
		},
	}
	if err := capture.Record(exchange); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}

	fileBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved trafficcapture.Exchange
	if err := json.Unmarshal(fileBytes, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Request.Header["Authorization"][0] != "[REDACTED]" ||
		saved.Request.Header["X-Client-Secret"][0] != "[REDACTED]" ||
		saved.Request.Header["X-Weave-User-Email"][0] != "[REDACTED]" {
		t.Fatalf("sensitive headers were not redacted: %#v", saved.Request.Header)
	}
	if saved.Request.Header["Content-Type"][0] != "application/json" || string(saved.Request.Body) != `{"prompt":"synthetic"}` {
		t.Fatalf("non-sensitive HTTP data changed: %#v", saved.Request)
	}
	if saved.Response == nil || saved.Response.Header["Content-Type"][0] != "application/json" ||
		saved.Response.Header["X-Session-Id"][0] != "[REDACTED]" ||
		saved.Response.Header["X-Client-Secret"][0] != "[REDACTED]" {
		t.Fatalf("unsafe response headers were not redacted: %#v", saved.Response)
	}
	fileInfo, err := os.Stat(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != trafficCaptureFilePermissions {
		t.Fatalf("capture mode = %o, want %o", fileInfo.Mode().Perm(), trafficCaptureFilePermissions)
	}
}

func TestTrafficCaptureWriterIncludesSensitiveHeadersOnlyWhenEnabled(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	t.Setenv(trafficCaptureSensitiveHeadersEnv, "true")
	capture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Record(trafficcapture.Exchange{
		SchemaVersion: 1,
		ID:            "inbound-2",
		Direction:     trafficcapture.DirectionInbound,
		Request: trafficcapture.Request{
			URL:    "https://provider.example/v1?api_key=query-secret",
			Header: map[string][]string{"Authorization": {"Bearer local-secret"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}

	fileBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileBytes), "Bearer local-secret") {
		t.Fatalf("explicit sensitive capture did not preserve the header: %s", fileBytes)
	}
	if strings.Contains(string(fileBytes), "query-secret") ||
		!strings.Contains(string(fileBytes), "api_key=%5BREDACTED%5D") {
		t.Fatalf("sensitive-header mode changed query-credential redaction: %s", fileBytes)
	}
}

func TestTrafficCaptureRedactsQueryCredentialsFromRequestErrors(t *testing.T) {
	requestErr := &url.Error{
		Op:  "Post",
		URL: "https://provider.example/v1?api_key=query-secret&safe=visible",
		Err: errors.New("provider unavailable"),
	}

	redactedError := trafficcapture.RedactRequestError(requestErr)
	if strings.Contains(redactedError, "query-secret") ||
		!strings.Contains(redactedError, "api_key=%5BREDACTED%5D") ||
		!strings.Contains(redactedError, "safe=visible") {
		t.Fatalf("request error redaction = %q", redactedError)
	}
}

func TestTrafficCaptureWriterStreamsFullBodySpoolsAndRedactsQueryCredentials(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	t.Setenv(trafficCaptureSensitiveHeadersEnv, "false")
	capture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	requestBody := bytes.Repeat([]byte("request-body-"), 128*1024)
	responseBody := bytes.Repeat([]byte("response-body-"), 128*1024)
	requestSpool := &trafficcapture.BodySpool{}
	requestSpool.Write(requestBody)
	responseSpool := &trafficcapture.BodySpool{}
	responseSpool.Write(responseBody)
	exchange := trafficcapture.Exchange{
		SchemaVersion: 1,
		ID:            "inbound-spooled",
		Direction:     trafficcapture.DirectionInbound,
		Request: trafficcapture.Request{
			Host:         "router.local",
			URL:          "https://provider.example/v1?model=fast&api_key=query-secret&key=google-secret&safe=a%2Fb",
			BodySpool:    requestSpool,
			BodyComplete: true,
		},
		Response: &trafficcapture.Response{
			BodySpool:    responseSpool,
			BodyComplete: true,
		},
		Complete: true,
	}
	if err := capture.Record(exchange); err != nil {
		t.Fatal(err)
	}
	if err := requestSpool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := responseSpool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}

	captureBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(captureBytes), "query-secret") || strings.Contains(string(captureBytes), "google-secret") {
		t.Fatalf("capture leaked query credentials: %s", captureBytes[:min(len(captureBytes), 500)])
	}
	var saved trafficcapture.Exchange
	if err := json.Unmarshal(captureBytes, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Request.URL != "https://provider.example/v1?model=fast&api_key=%5BREDACTED%5D&key=%5BREDACTED%5D&safe=a%2Fb" {
		t.Fatalf("captured URL = %q", saved.Request.URL)
	}
	if saved.Request.Host != "router.local" {
		t.Fatalf("captured request host = %q, want router.local", saved.Request.Host)
	}
	if !bytes.Equal(saved.Request.Body, requestBody) || saved.Response == nil || !bytes.Equal(saved.Response.Body, responseBody) {
		t.Fatal("capture did not preserve the complete spooled HTTP bodies")
	}
	if !saved.Request.BodyComplete || !saved.Response.BodyComplete || !saved.Complete {
		t.Fatalf("complete body capture marked incomplete: %#v", saved)
	}
}

func TestTrafficCaptureRejectsSymlinkCapturePathWithoutChangingTarget(t *testing.T) {
	temporaryDirectory := t.TempDir()
	targetPath := filepath.Join(temporaryDirectory, "target.jsonl")
	capturePath := filepath.Join(temporaryDirectory, "capture.jsonl")
	if err := os.WriteFile(targetPath, []byte("preserve target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, capturePath); err != nil {
		t.Fatal(err)
	}
	t.Setenv(trafficCaptureFileEnv, capturePath)

	if capture, err := newTrafficCaptureFromEnvironment(); err == nil {
		_ = capture.Close()
		t.Fatal("capture startup accepted a symlink path")
	}

	targetContents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetContents) != "preserve target" || targetInfo.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target changed: contents=%q permissions=%o", targetContents, targetInfo.Mode().Perm())
	}
}

func TestTrafficCaptureWriterSerializesConcurrentEvents(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	t.Setenv(trafficCaptureFileEnv, capturePath)
	t.Setenv(trafficCaptureSensitiveHeadersEnv, "false")
	capture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	const exchangeCount = 32
	var wait sync.WaitGroup
	for exchangeNumber := range exchangeCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := capture.Record(trafficcapture.Exchange{
				SchemaVersion: 1,
				ID:            trafficcapture.NewID(),
				Direction:     trafficcapture.DirectionUpstream,
				Attempt:       uint64(exchangeNumber + 1),
			}); err != nil {
				t.Errorf("record exchange: %v", err)
			}
		}()
	}
	wait.Wait()
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lineCount := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var exchange trafficcapture.Exchange
		if err := json.Unmarshal(scanner.Bytes(), &exchange); err != nil {
			t.Fatalf("invalid JSONL record: %v", err)
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lineCount != exchangeCount {
		t.Fatalf("capture lines = %d, want %d", lineCount, exchangeCount)
	}
}
