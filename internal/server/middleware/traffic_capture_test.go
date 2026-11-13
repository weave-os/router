package middleware_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/trafficcapture"

	"github.com/gin-gonic/gin"
)

type failingResponseWriter struct {
	gin.ResponseWriter
	writeErr error
}

func (writer *failingResponseWriter) Write([]byte) (int, error) {
	return 0, writer.writeErr
}

type exchangeRecorder struct {
	exchanges []trafficcapture.Exchange
}

func (r *exchangeRecorder) Record(exchange trafficcapture.Exchange) error {
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

func TestWithTrafficCapturePreservesRequestAndStreamedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &exchangeRecorder{}
	engine := gin.New()
	engine.Use(middleware.WithTrafficCapture(recorder))
	engine.POST("/v1/messages", func(c *gin.Context) {
		requestBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if string(requestBody) != `{"model":"test","stream":true}` {
			c.Status(http.StatusUnprocessableEntity)
			return
		}
		c.Writer.WriteHeader(http.StatusCreated)
		_, _ = c.Writer.Write([]byte("event: one\n\n"))
		c.Writer.Flush()
		_, _ = c.Writer.WriteString("event: two\n\n")
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=1", strings.NewReader(`{"model":"test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if response.Body.String() != "event: one\n\nevent: two\n\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	exchange := recorder.exchanges[0]
	if exchange.Direction != trafficcapture.DirectionInbound || exchange.ID == "" || !exchange.Complete {
		t.Fatalf("inbound exchange metadata = %#v", exchange)
	}
	if exchange.Request.URL != "/v1/messages?beta=1" || string(exchange.Request.Body) != `{"model":"test","stream":true}` {
		t.Fatalf("captured request = %#v", exchange.Request)
	}
	if exchange.Request.Host != "example.com" {
		t.Fatalf("captured request Host = %q, want example.com", exchange.Request.Host)
	}
	if exchange.Request.Header["Content-Type"][0] != "application/json" || !exchange.Request.BodyComplete {
		t.Fatalf("captured request headers/completion = %#v", exchange.Request)
	}
	if exchange.Response == nil || exchange.Response.StatusCode != http.StatusCreated || exchange.Response.Proto != request.Proto {
		t.Fatalf("captured response = %#v", exchange.Response)
	}
	if string(exchange.Response.Body) != response.Body.String() {
		t.Fatalf("captured response body = %q, sent body = %q", exchange.Response.Body, response.Body.String())
	}
}

func TestWithTrafficCaptureSkipsHealthAndAdminRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &exchangeRecorder{}
	engine := gin.New()
	engine.Use(middleware.WithTrafficCapture(recorder))
	engine.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.POST("/admin/v1/config", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/health", nil),
		httptest.NewRequest(http.MethodPost, "/admin/v1/config", strings.NewReader("secret")),
	} {
		engine.ServeHTTP(httptest.NewRecorder(), request)
	}
	if len(recorder.exchanges) != 0 {
		t.Fatalf("captured %d non-conversation exchanges, want 0", len(recorder.exchanges))
	}
}

func TestWithTrafficCaptureDoesNotReadRequestBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &exchangeRecorder{}
	engine := gin.New()
	engine.Use(middleware.WithTrafficCapture(recorder))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("unread payload"))
	engine.ServeHTTP(httptest.NewRecorder(), request)

	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	exchange := recorder.exchanges[0]
	if exchange.Request.BodyComplete || exchange.Complete || len(exchange.Request.Body) != 0 {
		t.Fatalf("unread request was incorrectly reported complete: %#v", exchange)
	}
}

func TestWithTrafficCaptureRecordsRecoveredHandlerPanic(t *testing.T) {
	recorder := &exchangeRecorder{}
	engine := gin.New()
	engine.Use(middleware.WithTrafficCapture(recorder), gin.Recovery())
	engine.POST("/v1/messages", func(*gin.Context) {
		panic("synthetic handler failure")
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"synthetic"}`))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	if recorder.exchanges[0].Response == nil || recorder.exchanges[0].Response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("captured panic response = %#v", recorder.exchanges[0].Response)
	}
}

func TestWithTrafficCaptureMarksFailedResponseWriteIncomplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &exchangeRecorder{}
	writeErr := errors.New("synthetic broken pipe")
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Writer = &failingResponseWriter{ResponseWriter: c.Writer, writeErr: writeErr}
		c.Next()
	}, middleware.WithTrafficCapture(recorder))
	engine.POST("/v1/messages", func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		_, _ = c.Writer.Write([]byte("partial response"))
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("synthetic request"))
	engine.ServeHTTP(httptest.NewRecorder(), request)

	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	exchange := recorder.exchanges[0]
	if exchange.Complete || exchange.Response == nil || exchange.Response.BodyComplete {
		t.Fatalf("failed response write was reported complete: %#v", exchange)
	}
	if !strings.Contains(exchange.Error, writeErr.Error()) {
		t.Fatalf("capture error = %q, want %q", exchange.Error, writeErr)
	}
}
