package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type tracingAPIKeyRepository struct {
	spanID trace.SpanID
}

func (*tracingAPIKeyRepository) Create(context.Context, auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (r *tracingAPIKeyRepository) GetActiveByHashWithInstallation(ctx context.Context, _ string) (*auth.APIKey, *auth.Installation, error) {
	r.spanID = trace.SpanContextFromContext(ctx).SpanID()
	return nil, nil, errors.New("database unavailable")
}

func (*tracingAPIKeyRepository) ListForInstallation(context.Context, string) ([]*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (*tracingAPIKeyRepository) MarkUsed(context.Context, string) error {
	return errors.New("not used")
}

func (*tracingAPIKeyRepository) SoftDelete(context.Context, string, string) (int64, error) {
	return 0, errors.New("not used")
}

func TestWithAuthEmitsSpanAroundRepositoryWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	previousTracer := middlewareFlowTracer
	middlewareFlowTracer = provider.Tracer("test")
	t.Cleanup(func() { middlewareFlowTracer = previousTracer })

	repo := &tracingAPIKeyRepository{}
	svc := auth.NewService(nil, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	engine := gin.New()
	var requestSpan trace.Span
	engine.Use(func(c *gin.Context) {
		ctx, span := provider.Tracer("test").Start(c.Request.Context(), "request")
		requestSpan = span
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		span.End()
	})
	engine.Use(WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(RouterKeyHeader, "rk_trace")
	req.Header.Set("Session-Id", "client-session-abc")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	authSpan := middlewareEndedSpanByName(t, recorder.Ended(), "router.auth")
	assert.Equal(t, requestSpan.SpanContext().SpanID(), authSpan.Parent().SpanID())
	assert.Equal(t, authSpan.SpanContext().SpanID(), repo.spanID)
	assert.Equal(t, "client-session-abc", middlewareSpanAttribute(t, authSpan.Attributes(), "client.session_id").AsString())
	assert.Equal(t, codes.Error, authSpan.Status().Code)
}

func TestRestoreRequestParentMakesFlowSpansAuthSiblings(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	parentCtx, requestSpan := provider.Tracer("test").Start(context.Background(), "request")
	authCtx, authSpan := provider.Tracer("test").Start(parentCtx, "router.auth")
	authSpan.End()
	routingCtx := restoreRequestParent(authCtx, parentCtx)
	_, routingSpan := provider.Tracer("test").Start(routingCtx, "router.routing")
	routingSpan.End()
	requestSpan.End()

	routing := middlewareEndedSpanByName(t, recorder.Ended(), "router.routing")
	assert.Equal(t, requestSpan.SpanContext().SpanID(), routing.Parent().SpanID())
	assert.NotEqual(t, authSpan.SpanContext().SpanID(), routing.Parent().SpanID())
}

func TestWithBillingSpanEndsBeforeDownstreamHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	previousTracer := middlewareFlowTracer
	middlewareFlowTracer = provider.Tracer("test")
	t.Cleanup(func() { middlewareFlowTracer = previousTracer })

	var requestSpan trace.Span
	var downstreamParent trace.SpanContext
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx, span := provider.Tracer("test").Start(c.Request.Context(), "request")
		requestSpan = span
		ctx = observability.WithClientSessionID(ctx, "client-session-abc")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		span.End()
	})
	engine.Use(WithBillingSpan(), WithBillingSpanEnd())
	engine.GET("/probe", func(c *gin.Context) {
		downstreamParent = trace.SpanContextFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))

	require.Equal(t, http.StatusOK, response.Code)
	billingSpan := middlewareEndedSpanByName(t, recorder.Ended(), "router.billing")
	assert.Equal(t, requestSpan.SpanContext().SpanID(), billingSpan.Parent().SpanID())
	assert.Equal(t, "client-session-abc", middlewareSpanAttribute(t, billingSpan.Attributes(), "client.session_id").AsString())
	assert.Equal(t, requestSpan.SpanContext().SpanID(), downstreamParent.SpanID())
	assert.Equal(t, codes.Ok, billingSpan.Status().Code)
}

func TestWithBillingSpanRestoresParentWhenGateAborts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	previousTracer := middlewareFlowTracer
	middlewareFlowTracer = provider.Tracer("test")
	t.Cleanup(func() { middlewareFlowTracer = previousTracer })

	var requestSpan trace.Span
	var afterParent trace.SpanContext
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx, span := provider.Tracer("test").Start(c.Request.Context(), "request")
		requestSpan = span
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		afterParent = trace.SpanContextFromContext(c.Request.Context())
		span.End()
	})
	engine.Use(WithBillingSpan())
	engine.Use(func(c *gin.Context) { c.AbortWithStatus(http.StatusPaymentRequired) })
	engine.Use(WithBillingSpanEnd())
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))

	require.Equal(t, http.StatusPaymentRequired, response.Code)
	billingSpan := middlewareEndedSpanByName(t, recorder.Ended(), "router.billing")
	assert.Equal(t, requestSpan.SpanContext().SpanID(), afterParent.SpanID())
	assert.Equal(t, codes.Error, billingSpan.Status().Code)
	assert.Equal(t, int64(http.StatusPaymentRequired), middlewareSpanAttribute(t, billingSpan.Attributes(), "http.response.status_code").AsInt64())
}

func middlewareEndedSpanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	require.FailNow(t, "span not found", "name=%s", name)
	return nil
}

func middlewareSpanAttribute(t *testing.T, attrs []attribute.KeyValue, key string) attribute.Value {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value
		}
	}
	require.FailNow(t, "span attribute not found", "key=%s", key)
	return attribute.Value{}
}
