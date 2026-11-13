package httputil

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/trafficcapture"
)

type trafficCaptureRoundTripper struct {
	transport http.RoundTripper
}

func (t *trafficCaptureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	scope := trafficcapture.FromContext(request.Context())
	if scope == nil {
		return t.transport.RoundTrip(request)
	}

	startedAt := time.Now()
	exchangeID, attempt := scope.NextAttempt()
	requestBody := captureRequestBody(request)
	requestHost := request.Host
	if requestHost == "" {
		requestHost = request.URL.Host
	}
	exchangeRequest := trafficcapture.Request{
		Method:           request.Method,
		URL:              request.URL.String(),
		Host:             requestHost,
		Proto:            request.Proto,
		Header:           cloneHTTPHeaders(request.Header),
		ContentLength:    request.ContentLength,
		TransferEncoding: append([]string(nil), request.TransferEncoding...),
		BodySpool:        requestBody.spool,
	}

	response, transportErr := t.transport.RoundTrip(request)
	var responseRecord *trafficcapture.Response
	var responseBodySpool *trafficcapture.BodySpool
	if transportErr == nil {
		responseRecord = &trafficcapture.Response{
			Proto:            response.Proto,
			StatusCode:       response.StatusCode,
			Header:           cloneHTTPHeaders(response.Header),
			ContentLength:    response.ContentLength,
			TransferEncoding: append([]string(nil), response.TransferEncoding...),
		}
		if response.Body != nil && response.Body != http.NoBody {
			responseBodySpool = &trafficcapture.BodySpool{}
			responseRecord.BodySpool = responseBodySpool
		}
	}
	finishResponse := func(responseComplete bool, responseErr error) {
		// RoundTrip may return while the transport is still consuming/closing
		// the request. Retain both spools until both bodies have finished.
		requestBody.whenFinished(func(requestComplete bool, requestBodyErr error) {
			exchangeRequest.BodyComplete = requestComplete && requestBody.spoolError() == nil
			if responseBodySpool != nil && responseBodySpool.Err() != nil {
				responseComplete = false
			}
			if responseRecord != nil {
				responseRecord.BodyComplete = responseComplete
			}
			exchange := trafficcapture.Exchange{
				SchemaVersion: 1,
				ID:            exchangeID,
				ParentID:      scope.ExchangeID(),
				Attempt:       attempt,
				Direction:     trafficcapture.DirectionUpstream,
				StartedAt:     startedAt,
				DurationMS:    time.Since(startedAt).Milliseconds(),
				Request:       exchangeRequest,
				Response:      responseRecord,
				Complete:      exchangeRequest.BodyComplete && responseComplete,
			}
			if transportErr != nil {
				exchange.Error = trafficcapture.RedactRequestError(transportErr)
			}
			if requestBodyErr != nil {
				exchange.Error = joinCaptureError(exchange.Error, "read upstream request body: "+requestBodyErr.Error())
			}
			if spoolErr := requestBody.spoolError(); spoolErr != nil {
				exchange.Error = joinCaptureError(exchange.Error, spoolErr.Error())
			}
			if responseErr != nil {
				exchange.Error = joinCaptureError(exchange.Error, "read upstream response body: "+responseErr.Error())
			}
			if responseBodySpool != nil {
				if spoolErr := responseBodySpool.Err(); spoolErr != nil {
					exchange.Error = joinCaptureError(exchange.Error, spoolErr.Error())
				}
			}
			recordUpstreamExchange(request.Context(), scope, exchange)
			closeBodySpool(request.Context(), exchangeID, responseBodySpool)
			closeBodySpool(request.Context(), exchangeID, requestBody.spool)
		})
	}
	if transportErr != nil {
		finishResponse(false, nil)
		return nil, transportErr
	}
	if responseBodySpool == nil {
		finishResponse(true, nil)
		return response, nil
	}
	response.Body = &trafficCaptureBody{
		ReadCloser:     response.Body,
		spool:          responseBodySpool,
		expectedLength: response.ContentLength,
		finish:         finishResponse,
	}
	return response, nil
}

type requestBodyCapture struct {
	mu       sync.Mutex
	spool    *trafficcapture.BodySpool
	complete bool
	readErr  error
	finished bool
	onFinish func(bool, error)
}

func captureRequestBody(request *http.Request) *requestBodyCapture {
	capture := &requestBodyCapture{spool: &trafficcapture.BodySpool{}}
	if request.Body == nil || request.Body == http.NoBody {
		capture.complete = true
		capture.finished = true
		capture.spool = nil
		return capture
	}
	request.Body = &trafficCaptureBody{
		ReadCloser:     request.Body,
		spool:          capture.spool,
		expectedLength: request.ContentLength,
		finish: func(complete bool, readErr error) {
			capture.mu.Lock()
			capture.complete = complete
			capture.readErr = readErr
			capture.finished = true
			onFinish := capture.onFinish
			capture.mu.Unlock()
			if onFinish != nil {
				onFinish(complete, readErr)
			}
		},
	}
	return capture
}

func (capture *requestBodyCapture) whenFinished(onFinish func(bool, error)) {
	capture.mu.Lock()
	if !capture.finished {
		capture.onFinish = onFinish
		capture.mu.Unlock()
		return
	}
	complete, readErr := capture.complete, capture.readErr
	capture.mu.Unlock()
	onFinish(complete, readErr)
}

func (capture *requestBodyCapture) spoolError() error {
	if capture.spool == nil {
		return nil
	}
	return capture.spool.Err()
}

func cloneHTTPHeaders(headers http.Header) map[string][]string {
	cloned := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func recordUpstreamExchange(ctx context.Context, scope *trafficcapture.Scope, exchange trafficcapture.Exchange) {
	if err := scope.Recorder().Record(exchange); err != nil {
		observability.FromContext(ctx).Error("Failed to record local HTTP traffic", "exchange_id", exchange.ID, "parent_exchange_id", exchange.ParentID, "direction", trafficcapture.DirectionUpstream, "err", err)
	}
}

func closeBodySpool(ctx context.Context, exchangeID string, spool *trafficcapture.BodySpool) {
	if spool == nil {
		return
	}
	if err := spool.Close(); err != nil {
		observability.FromContext(ctx).Error("Failed to remove temporary local HTTP body capture", "exchange_id", exchangeID, "err", err)
	}
}

func joinCaptureError(existingError, newError string) string {
	if existingError == "" {
		return newError
	}
	return existingError + "; " + newError
}

type trafficCaptureBody struct {
	io.ReadCloser
	spool          *trafficcapture.BodySpool
	expectedLength int64
	mu             sync.Mutex
	bytesRead      int64
	complete       bool
	readErr        error
	once           sync.Once
	finish         func(bool, error)
}

func (body *trafficCaptureBody) Read(buffer []byte) (int, error) {
	body.mu.Lock()
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.spool.Write(buffer[:count])
	}
	body.bytesRead += int64(count)
	if err == io.EOF {
		if body.expectedLength > 0 && body.bytesRead != body.expectedLength {
			body.readErr = io.ErrUnexpectedEOF
		} else {
			body.complete = true
		}
	} else if err == nil && body.expectedLength > 0 && body.bytesRead == body.expectedLength {
		body.complete = true
	} else if err != nil {
		body.readErr = err
	}
	body.mu.Unlock()
	if err == io.EOF || err != nil {
		body.finalize()
	}
	return count, err
}

func (body *trafficCaptureBody) Close() error {
	err := body.ReadCloser.Close()
	if err != nil {
		body.mu.Lock()
		body.readErr = err
		body.mu.Unlock()
	}
	body.mu.Lock()
	if body.readErr == nil && body.expectedLength > 0 && body.bytesRead == body.expectedLength {
		body.complete = true
	}
	body.mu.Unlock()
	body.finalize()
	return err
}

func (body *trafficCaptureBody) finalize() {
	body.once.Do(func() {
		body.mu.Lock()
		complete := body.complete && body.readErr == nil
		readErr := body.readErr
		body.mu.Unlock()
		body.finish(complete, readErr)
	})
}
