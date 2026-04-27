package otelexport

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	goslog "log/slog"
)

type Config struct {
	ServiceName  string
	OTLPEndpoint string
	OTLPHeaders  string
}

type Providers struct {
	TracerProvider *sdktrace.TracerProvider
	LoggerProvider *sdklog.LoggerProvider
}

func ParseHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	if raw == "" {
		return headers
	}
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

func Setup(ctx context.Context, cfg Config) (*Providers, func(), error) {
	headers := ParseHeaders(cfg.OTLPHeaders)

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, nil, err
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/traces"),
		otlptracehttp.WithHeaders(headers),
	}
	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
	)
	otel.SetTracerProvider(tp)

	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/logs"),
		otlploghttp.WithHeaders(headers),
	}
	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)

	cleanup := func() {
		_ = tp.Shutdown(context.Background())
		_ = lp.Shutdown(context.Background())
	}

	return &Providers{TracerProvider: tp, LoggerProvider: lp}, cleanup, nil
}

func SetupSync(ctx context.Context, cfg Config) (*Providers, func(), error) {
	headers := ParseHeaders(cfg.OTLPHeaders)

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, nil, err
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/traces"),
		otlptracehttp.WithHeaders(headers),
	}
	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(traceExp),
	)
	otel.SetTracerProvider(tp)

	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/logs"),
		otlploghttp.WithHeaders(headers),
	}
	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExp)),
	)

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
	}

	return &Providers{TracerProvider: tp, LoggerProvider: lp}, cleanup, nil
}

func NewTracer(tp trace.TracerProvider, name string) trace.Tracer {
	return tp.Tracer(name)
}

func NewSlogLogger(lp log.LoggerProvider, name string) *goslog.Logger {
	return otelslog.NewLogger(name, otelslog.WithLoggerProvider(lp))
}
