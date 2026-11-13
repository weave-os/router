package httputil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/trafficcapture"

	"github.com/stretchr/testify/require"
)

type closingRequestReader struct {
	started chan struct{}
	closed  chan struct{}
	release chan struct{}
}

func (reader *closingRequestReader) Read(buffer []byte) (int, error) {
	close(reader.started)
	<-reader.release
	return copy(buffer, "last bytes"), io.EOF
}

func (reader *closingRequestReader) Close() error {
	close(reader.closed)
	return nil
}

func TestRequestCaptureCloseWaitsForInFlightRead(t *testing.T) {
	reader := &closingRequestReader{started: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{})}
	request, err := http.NewRequest(http.MethodPost, "https://provider.example", reader)
	require.NoError(t, err)
	capture := captureRequestBody(request)
	t.Cleanup(func() { require.NoError(t, capture.spool.Close()) })
	capturedBody := make(chan string, 1)
	capture.whenFinished(func(_ bool, _ error) {
		bodyReader, readErr := capture.spool.Reader()
		if readErr != nil {
			t.Error(readErr)
			return
		}
		body, readErr := io.ReadAll(bodyReader)
		if readErr != nil {
			t.Error(readErr)
		}
		capturedBody <- string(body)
	})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = request.Body.Read(make([]byte, 32))
	}()
	<-reader.started
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		_ = request.Body.Close()
	}()
	<-reader.closed
	close(reader.release)
	select {
	case body := <-capturedBody:
		require.Equal(t, "last bytes", body)
	case <-time.After(5 * time.Second):
		t.Fatal("request capture deadlocked while closing a concurrent read")
	}
	<-readDone
	<-closeDone
}

func TestCaptureWaitsForRequestAfterEarlyResponse(t *testing.T) {
	for _, responseKind := range []string{"empty", "body", "transport error"} {
		for _, consumeRequest := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/consume=%t", responseKind, consumeRequest), func(t *testing.T) {
				recorder := &capturedExchanges{}
				var pendingRequest io.ReadCloser
				transportErr := errors.New("synthetic transport failure")
				client := NewClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
					pendingRequest = request.Body
					if responseKind == "transport error" {
						return nil, transportErr
					}
					response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}
					if responseKind == "body" {
						response.Body = io.NopCloser(strings.NewReader("synthetic response"))
						response.ContentLength = int64(len("synthetic response"))
					}
					return response, nil
				}))
				ctx := trafficcapture.WithRecorder(context.Background(), recorder, "early-response")
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("synthetic request"))
				require.NoError(t, err)
				response, err := client.Do(request)
				if responseKind == "transport error" {
					require.ErrorIs(t, err, transportErr)
				} else {
					require.NoError(t, err)
					_, err = io.Copy(io.Discard, response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
				}
				if len(recorder.exchanges) != 0 {
					t.Error("recorded an exchange before the transport finished its request body")
				}
				if consumeRequest {
					body, err := io.ReadAll(pendingRequest)
					require.NoError(t, err)
					require.Equal(t, "synthetic request", string(body))
				}
				require.NoError(t, pendingRequest.Close())
				require.Len(t, recorder.exchanges, 1)
				exchange := recorder.exchanges[0]
				require.Equal(t, consumeRequest, exchange.Request.BodyComplete)
				require.Equal(t, consumeRequest && responseKind != "transport error", exchange.Complete)
				if consumeRequest {
					require.Equal(t, "synthetic request", string(exchange.Request.Body))
				} else {
					require.Empty(t, exchange.Request.Body)
				}
			})
		}
	}
}

type capturedExchanges struct {
	exchanges []trafficcapture.Exchange
}

func (r *capturedExchanges) Record(exchange trafficcapture.Exchange) error {
	if exchange.Request.BodySpool != nil {
		bodyReader, err := exchange.Request.BodySpool.Reader()
		if err != nil {
			return err
		}
		exchange.Request.Body, err = io.ReadAll(bodyReader)
		if err != nil {
			return err
		}
		exchange.Request.BodySpool = nil
	}
	if exchange.Response != nil && exchange.Response.BodySpool != nil {
		bodyReader, err := exchange.Response.BodySpool.Reader()
		if err != nil {
			return err
		}
		exchange.Response.Body, err = io.ReadAll(bodyReader)
		if err != nil {
			return err
		}
		exchange.Response.BodySpool = nil
	}
	r.exchanges = append(r.exchanges, exchange)
	return nil
}

func TestNewClientCapturesUpstreamRequestAndStreamedResponse(t *testing.T) {
	recorder := &capturedExchanges{}
	requestPayload := []byte(`{"model":"test","stream":true}`)
	responsePayload := []byte("data: first\n\ndata: second\n\n")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		actualRequestBody, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(actualRequestBody, requestPayload) {
			t.Fatalf("provider received request body %q, want %q", actualRequestBody, requestPayload)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
			ContentLength: int64(len(responsePayload)),
			Body:          io.NopCloser(bytes.NewReader(responsePayload)),
		}, nil
	})
	client := NewClient(transport)
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-1")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", bytes.NewReader(requestPayload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	actualResponseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualResponseBody, responsePayload) {
		t.Fatalf("client received response body %q, want %q", actualResponseBody, responsePayload)
	}
	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	exchange := recorder.exchanges[0]
	if exchange.Direction != trafficcapture.DirectionUpstream || exchange.ParentID != "inbound-1" || exchange.Attempt != 1 || !exchange.Complete {
		t.Fatalf("captured upstream metadata = %#v", exchange)
	}
	if !bytes.Equal(exchange.Request.Body, requestPayload) || exchange.Request.Header["Accept"][0] != "application/json" ||
		exchange.Request.Host != "provider.example" {
		t.Fatalf("captured provider request = %#v", exchange.Request)
	}
	if exchange.Response == nil || exchange.Response.StatusCode != http.StatusOK || !bytes.Equal(exchange.Response.Body, responsePayload) {
		t.Fatalf("captured provider response = %#v", exchange.Response)
	}
}

func TestNewClientCapturesEachUpstreamRetrySeparately(t *testing.T) {
	recorder := &capturedExchanges{}
	client := NewClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_ = request.Body.Close()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody}, nil
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-2")
	for range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if len(recorder.exchanges) != 2 || recorder.exchanges[0].Attempt != 1 || recorder.exchanges[1].Attempt != 2 {
		t.Fatalf("captured attempts = %#v, want separate attempts 1 and 2", recorder.exchanges)
	}
}

func TestNewClientRecordsTransportFailureWithoutChangingError(t *testing.T) {
	recorder := &capturedExchanges{}
	upstreamErr := errors.New("provider unavailable")
	client := NewClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_ = request.Body.Close()
		return nil, upstreamErr
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-3")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("client error = %v, want wrapped provider error", err)
	}
	if len(recorder.exchanges) != 1 || !strings.Contains(recorder.exchanges[0].Error, upstreamErr.Error()) {
		t.Fatalf("captured transport failure = %#v", recorder.exchanges)
	}
}

func TestNewClientRecordsPartialResponseWhenClientClosesEarly(t *testing.T) {
	recorder := &capturedExchanges{}
	client := NewClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_ = request.Body.Close()
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			ContentLength: int64(len("all bytes")),
			Body:          io.NopCloser(strings.NewReader("all bytes")),
		}, nil
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-4")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 3)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(recorder.exchanges) != 1 || recorder.exchanges[0].Complete {
		t.Fatalf("partial response exchange = %#v, want incomplete", recorder.exchanges)
	}
	if string(recorder.exchanges[0].Response.Body) != "all" {
		t.Fatalf("partial response body = %q, want all", recorder.exchanges[0].Response.Body)
	}
}
