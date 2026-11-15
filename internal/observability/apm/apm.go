package apm

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	otelgin "go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"

	"weave-os/router/internal/config"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/version"
)

const serviceName = "router"

var (
	initOnce       sync.Once
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
)

func Init() {
	initOnce.Do(initLocked)
}

func initLocked() {
	log := observability.Get()

	otel.SetTextMapPropagator(propagation.TraceContext{})

	endpoint := config.GetOr("WV_APM_OTLP_ENDPOINT", "")
	if endpoint == "" {
		log.Info("APM disabled: WV_APM_OTLP_ENDPOINT unset")
		return
	}

	ctx := context.Background()
	deployEnv := config.GetOr("ROUTER_DEPLOYMENT_ENV", config.GetOr("ENV", "dev"))

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version.Commit),
			semconv.DeploymentEnvironment(deployEnv),
		),
		resource.WithHost(),
		resource.WithContainer(),
	)
	if err != nil {
		log.Error("APM init: resource construction failed", "err", err)
		return
	}

	insecure := config.GetOr("WV_APM_OTLP_INSECURE", "false") == "true"

	traceExporter, err := newTraceExporter(ctx, endpoint, insecure)
	if err != nil {
		log.Error("APM init: trace exporter failed", "err", err)
		return
	}
	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx, metricGRPCOpts(endpoint, insecure)...)
	if err != nil {
		log.Error("APM init: metric exporter failed", "err", err)
		return
	}
	meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(60*time.Second),
		)),
	)
	otel.SetMeterProvider(meterProvider)

	if err := otelruntime.Start(otelruntime.WithMinimumReadMemStatsInterval(15 * time.Second)); err != nil {
		log.Warn("APM init: runtime instrumentation failed; SDK traces still active", "err", err)
	}

	log.Info("APM enabled",
		"endpoint", endpoint,
		"service", serviceName,
		"deployment_env", deployEnv,
		"version", version.Commit,
		"insecure", insecure,
	)
}

func newTraceExporter(ctx context.Context, endpoint string, insecure bool) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return otlptrace.New(ctx, otlptracegrpc.NewClient(opts...))
}

func metricGRPCOpts(endpoint string, insecure bool) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return opts
}

func Middleware() gin.HandlerFunc {
	return otelgin.Middleware(
		serviceName,
		otelgin.WithGinFilter(func(c *gin.Context) bool {
			path := c.FullPath()
			return path != "/health" && path != "/readyz" && path != "/startupz" && path != "/validate"
		}),
	)
}

func ShutdownWithContext(ctx context.Context) {
	if tracerProvider != nil {
		_ = tracerProvider.Shutdown(ctx)
	}
	if meterProvider != nil {
		_ = meterProvider.Shutdown(ctx)
	}
}
