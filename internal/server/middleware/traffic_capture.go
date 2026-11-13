package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/trafficcapture"

	"github.com/gin-gonic/gin"
)

const (
	trafficCaptureAnthropicMessagesPath = "/v1/messages"
	trafficCaptureOpenAIChatPath        = "/v1/chat/completions"
	trafficCaptureOpenAIResponsesPath   = "/v1/responses"
	trafficCaptureGeminiModelsPrefix    = "/v1beta/models/"
	trafficCaptureRoutePrefix           = "/v1/route"
)

// WithTrafficCapture records conversation HTTP exchanges without changing the
// body or streaming behavior seen by handlers and clients.
func WithTrafficCapture(recorder trafficcapture.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isConversationRequest(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}

		startedAt := time.Now()
		exchangeID := trafficcapture.NewID()
		requestBody := &inboundRequestBodyCapture{
			spool:          &trafficcapture.BodySpool{},
			expectedLength: c.Request.ContentLength,
		}
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			requestBody.complete = true
			requestBody.spool = nil
		} else {
			c.Request.Body = &inboundRequestBodyReader{ReadCloser: c.Request.Body, capture: requestBody}
		}
		c.Request = c.Request.WithContext(trafficcapture.WithRecorder(c.Request.Context(), recorder, exchangeID))

		request := trafficcapture.Request{
			Method:           c.Request.Method,
			URL:              c.Request.URL.String(),
			Host:             c.Request.Host,
			Proto:            c.Request.Proto,
			Header:           cloneHeaders(c.Request.Header),
			ContentLength:    c.Request.ContentLength,
			TransferEncoding: append([]string(nil), c.Request.TransferEncoding...),
			BodySpool:        requestBody.spool,
		}
		responseWriter := &trafficCaptureResponseWriter{
			ResponseWriter: c.Writer,
			bodySpool:      &trafficcapture.BodySpool{},
		}
		defer func() {
			closeTrafficCaptureSpool(c, exchangeID, requestBody.spool)
			closeTrafficCaptureSpool(c, exchangeID, responseWriter.bodySpool)
		}()
		c.Writer = responseWriter
		c.Next()

		request.BodyComplete = requestBody.complete && bodySpoolError(requestBody.spool) == nil

		statusCode := responseWriter.Status()
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		responseHeaders := cloneHeaders(c.Writer.Header())
		responseContentLength := int64(-1)
		if length, err := strconv.ParseInt(c.Writer.Header().Get("Content-Length"), 10, 64); err == nil {
			responseContentLength = length
		}
		responseWriteErr := responseWriter.completionError(responseContentLength)
		responseBodyComplete := responseWriteErr == nil && responseWriter.bodySpool.Err() == nil
		exchange := trafficcapture.Exchange{
			SchemaVersion: 1,
			ID:            exchangeID,
			Direction:     trafficcapture.DirectionInbound,
			StartedAt:     startedAt,
			DurationMS:    time.Since(startedAt).Milliseconds(),
			Request:       request,
			Response: &trafficcapture.Response{
				Proto:         c.Request.Proto,
				StatusCode:    statusCode,
				Header:        responseHeaders,
				ContentLength: responseContentLength,
				BodySpool:     responseWriter.bodySpool,
				BodyComplete:  responseBodyComplete,
			},
			Complete: request.BodyComplete && responseBodyComplete,
		}
		if requestBody.readErr != nil {
			exchange.Error = "read inbound request body: " + requestBody.readErr.Error()
		}
		if spoolErr := bodySpoolError(requestBody.spool); spoolErr != nil {
			exchange.Error = joinTrafficCaptureError(exchange.Error, spoolErr.Error())
		}
		if responseWriteErr != nil {
			exchange.Error = joinTrafficCaptureError(exchange.Error, "write inbound response body: "+responseWriteErr.Error())
		}
		if spoolErr := responseWriter.bodySpool.Err(); spoolErr != nil {
			exchange.Error = joinTrafficCaptureError(exchange.Error, spoolErr.Error())
		}
		if err := recorder.Record(exchange); err != nil {
			observability.FromGin(c).Error("Failed to record local HTTP traffic", "exchange_id", exchangeID, "direction", trafficcapture.DirectionInbound, "err", err)
		}
	}
}

type inboundRequestBodyCapture struct {
	spool          *trafficcapture.BodySpool
	expectedLength int64
	bytesRead      int64
	complete       bool
	readErr        error
}

type inboundRequestBodyReader struct {
	io.ReadCloser
	capture *inboundRequestBodyCapture
}

func (r *inboundRequestBodyReader) Read(p []byte) (int, error) {
	count, err := r.ReadCloser.Read(p)
	if count > 0 {
		r.capture.spool.Write(p[:count])
	}
	r.capture.bytesRead += int64(count)
	if err == io.EOF {
		if r.capture.expectedLength > 0 && r.capture.bytesRead != r.capture.expectedLength {
			r.capture.readErr = io.ErrUnexpectedEOF
		} else {
			r.capture.complete = true
		}
	} else if err == nil && r.capture.expectedLength > 0 && r.capture.bytesRead == r.capture.expectedLength {
		r.capture.complete = true
	} else if err != nil {
		r.capture.readErr = err
	}
	return count, err
}

func isConversationRequest(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	return path == trafficCaptureAnthropicMessagesPath ||
		strings.HasPrefix(path, trafficCaptureAnthropicMessagesPath+"/") ||
		path == trafficCaptureOpenAIChatPath ||
		path == trafficCaptureOpenAIResponsesPath ||
		strings.HasPrefix(path, trafficCaptureGeminiModelsPrefix) ||
		path == trafficCaptureRoutePrefix ||
		strings.HasPrefix(path, trafficCaptureRoutePrefix+"/")
}

func cloneHeaders(headers http.Header) map[string][]string {
	cloned := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

type trafficCaptureResponseWriter struct {
	gin.ResponseWriter
	bodySpool    *trafficcapture.BodySpool
	writeErr     error
	bytesWritten int64
}

func (w *trafficCaptureResponseWriter) Write(body []byte) (int, error) {
	// Conversation endpoints return protocol JSON or SSE; escaping would corrupt their wire format.
	// lgtm [go/reflected-xss]
	written, err := w.ResponseWriter.Write(body)
	w.bytesWritten += int64(written)
	if written > 0 {
		w.bodySpool.Write(body[:written])
	}
	if err != nil {
		w.writeErr = err
	} else if written != len(body) {
		w.writeErr = io.ErrShortWrite
	}
	return written, err
}

func (w *trafficCaptureResponseWriter) completionError(contentLength int64) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	if contentLength >= 0 && w.bytesWritten != contentLength {
		return fmt.Errorf("response wrote %d bytes but Content-Length is %d", w.bytesWritten, contentLength)
	}
	return nil
}

func bodySpoolError(spool *trafficcapture.BodySpool) error {
	if spool == nil {
		return nil
	}
	return spool.Err()
}

func joinTrafficCaptureError(existingError, newError string) string {
	if existingError == "" {
		return newError
	}
	return existingError + "; " + newError
}

func closeTrafficCaptureSpool(c *gin.Context, exchangeID string, spool *trafficcapture.BodySpool) {
	if spool == nil {
		return
	}
	if err := spool.Close(); err != nil {
		observability.FromGin(c).Error("Failed to remove temporary local HTTP body capture", "exchange_id", exchangeID, "err", err)
	}
}

func (w *trafficCaptureResponseWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}
