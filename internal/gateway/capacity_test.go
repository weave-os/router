package gateway_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/health"
	"weave-os/router/internal/requestcontext"
)

type countingBody struct {
	io.Reader
	reads int
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (*countingBody) Close() error { return nil }

func TestGatewayCapacityRetainsPermitsAndBuffersThroughStreamingCancellation(t *testing.T) {
	workerCancelled := make(chan struct{})
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(workerCancelled)
	}))
	defer worker.Close()
	forwarder, admissions, _ := gatewayFixture(t, worker, nil, nil)
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1, MaxBufferedBytes: 2048, ResumeBufferedBytes: 1024})
	require.NoError(t, err)
	forwarder.SetCapacity(capacity)
	finished := make(chan struct{})
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		forwarder.ServeHTTP(w, r)
	}))
	defer entry.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL+"/v1/responses", strings.NewReader(`{"input":"synthetic capacity stream","stream":true}`))
	require.NoError(t, err)
	request.Header.Set(auth.RouterKeyHeader, "rk_credential")
	request.Header.Set("Session-Id", "first-stream")
	response, err := entry.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: first\n", line)
	assert.Equal(t, 1, capacity.Snapshot().ActiveRequests)
	assert.Positive(t, capacity.Snapshot().BufferedBytes)
	assert.False(t, capacity.Snapshot().Ready)
	body := &countingBody{Reader: strings.NewReader(`{"input":"second"}`)}
	second := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	second.Header.Set("Session-Id", "must-not-admit")
	rejected := httptest.NewRecorder()
	forwarder.ServeHTTP(rejected, second)
	assert.Equal(t, http.StatusServiceUnavailable, rejected.Code)
	assert.Equal(t, "1", rejected.Header().Get("Retry-After"))
	assert.Zero(t, body.reads)
	assert.Equal(t, "first-stream", admissions.seenConversation)
	cancel()
	for _, completed := range []<-chan struct{}{finished, workerCancelled} {
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("stream work did not finish after cancellation")
		}
	}
	assert.Zero(t, capacity.Snapshot().ActiveRequests)
	assert.Zero(t, capacity.Snapshot().BufferedBytes)
	assert.True(t, capacity.Snapshot().Ready)
}

func TestGatewayRejectsBufferGrowthBeforeAdmissionAndReleasesReservation(t *testing.T) {
	var workerCalls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		workerCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()
	for _, knownLength := range []bool{true, false} {
		t.Run(map[bool]string{true: "known_length", false: "chunked"}[knownLength], func(t *testing.T) {
			forwarder, admissions, _ := gatewayFixture(t, worker, nil, nil)
			capacity, err := health.NewCapacity(health.Limits{MaxRequests: 2, ResumeRequests: 1, MaxBufferedBytes: 1500, ResumeBufferedBytes: 750})
			require.NoError(t, err)
			forwarder.SetCapacity(capacity)
			payload := `{"input":"` + strings.Repeat("x", 2048) + `"}`
			body := &countingBody{Reader: strings.NewReader(payload)}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
			request.Header.Set("Session-Id", "must-not-admit")
			if knownLength {
				request.ContentLength = int64(len(payload))
			} else {
				request.ContentLength = -1
			}
			response := httptest.NewRecorder()
			forwarder.ServeHTTP(response, request)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			if knownLength {
				assert.Zero(t, body.reads)
			}
			assert.Empty(t, admissions.seenConversation)
			assert.Zero(t, capacity.Snapshot().ActiveRequests)
			assert.Zero(t, capacity.Snapshot().BufferedBytes)
			assert.True(t, capacity.Snapshot().Ready)
			// A rejected large request must leave room for later ordinary work.
			response = httptest.NewRecorder()
			forwarder.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"small"}`)))
			assert.Equal(t, http.StatusOK, response.Code)
		})
	}
	assert.Equal(t, int32(2), workerCalls.Load())
}

func TestGatewayBodySizeAndReadFailureReleaseCapacity(t *testing.T) {
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid body reached worker") }))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(t, worker, nil, nil)
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1, MaxBufferedBytes: 2048, ResumeBufferedBytes: 1024})
	require.NoError(t, err)
	forwarder.SetCapacity(capacity)
	for _, test := range []struct {
		name   string
		body   io.ReadCloser
		length int64
		status int
	}{
		{"too_large", &countingBody{Reader: strings.NewReader("must not read")}, requestcontext.MaxRequestBodyBytes + 1, http.StatusRequestEntityTooLarge},
		{"read_error", io.NopCloser(failingReader{}), -1, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", test.body)
			request.ContentLength = test.length
			response := httptest.NewRecorder()
			forwarder.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code)
			assert.Zero(t, capacity.Snapshot().BufferedBytes)
			assert.Zero(t, capacity.Snapshot().ActiveRequests)
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// BenchmarkGatewayCapacityStreams holds real proxy streams while filling the
// configured body budget. It uses only synthetic input and a local TLS worker.
func BenchmarkGatewayCapacityStreams(b *testing.B) {
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: accepted\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(b, worker, nil, nil)
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 500, ResumeRequests: 400, MaxBufferedBytes: 128 << 20, ResumeBufferedBytes: 96 << 20})
	require.NoError(b, err)
	forwarder.SetCapacity(capacity)
	completed := make(chan struct{}, 32)
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/capacityz" {
			capacity.Handler().ServeHTTP(w, r)
			return
		}
		defer func() { completed <- struct{}{} }()
		forwarder.ServeHTTP(w, r)
	}))
	defer entry.Close()
	payload := `{"input":"` + strings.Repeat("x", 16<<20) + `","stream":true}`
	var peakBuffered int64
	var peakRequests, rejections int
	var slowestProbe time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var responses []*http.Response
		var cancellations []context.CancelFunc
		requests := 0
		for range 16 {
			ctx, cancel := context.WithTimeout(b.Context(), 30*time.Second)
			cancellations = append(cancellations, cancel)
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL+"/v1/responses", strings.NewReader(payload))
			require.NoError(b, err)
			response, err := entry.Client().Do(request)
			require.NoError(b, err)
			responses = append(responses, response)
			requests++
			if response.StatusCode == http.StatusServiceUnavailable {
				rejections++
				break
			}
			require.Equal(b, http.StatusOK, response.StatusCode)
			_, err = bufio.NewReader(response.Body).ReadString('\n')
			require.NoError(b, err)
			snapshot := capacity.Snapshot()
			peakBuffered = max(peakBuffered, snapshot.BufferedBytes)
			peakRequests = max(peakRequests, snapshot.ActiveRequests)
		}
		probeStart := time.Now()
		probe, err := entry.Client().Get(entry.URL + "/capacityz")
		require.NoError(b, err)
		require.Equal(b, http.StatusServiceUnavailable, probe.StatusCode)
		_, _ = io.Copy(io.Discard, probe.Body)
		probe.Body.Close()
		slowestProbe = max(slowestProbe, time.Since(probeStart))
		for _, cancel := range cancellations {
			cancel()
		}
		for _, response := range responses {
			response.Body.Close()
		}
		for range requests {
			select {
			case <-completed:
			case <-time.After(5 * time.Second):
				b.Fatal("gateway retained work after cancellation")
			}
		}
		require.True(b, capacity.Snapshot().Ready)
		require.Zero(b, capacity.Snapshot().BufferedBytes)
		require.Zero(b, capacity.Snapshot().ActiveRequests)
	}
	b.StopTimer()
	b.ReportMetric(float64(peakBuffered)/(1<<20), "peak_buffer_MiB")
	b.ReportMetric(float64(peakRequests), "peak_streams")
	b.ReportMetric(float64(rejections)/float64(b.N), "rejections/batch")
	b.ReportMetric(float64(slowestProbe.Microseconds()), "max_probe_us")
	b.SetBytes(int64(len(payload)) * int64(peakRequests))
}
