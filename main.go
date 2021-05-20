package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"google.golang.org/grpc"
)

var addr = "127.0.0.1:8000"
var tracer trace.Tracer
var httpClient http.Client
var logger log.Logger

var metricRequestLatency = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "demo",
	Name:      "request_latency_seconds",
	Help:      "Request Latency",
	Buckets:   prometheus.ExponentialBuckets(.0001, 2, 50),
})

func initTracer() func() {

	ctx := context.Background()
	driver := otlpgrpc.NewDriver(
		otlpgrpc.WithInsecure(),
		otlpgrpc.WithEndpoint("tempo:55680"),
		otlpgrpc.WithDialOption(grpc.WithBlock()),
	)

	exporter, err := otlp.NewExporter(ctx, driver)
	if err != nil {
		log.Fatal(err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("frontend-go"),
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	bsp := trace.NewBatchSpanProcessor(exporter)
	provider := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithResource(res),
		trace.WithSpanProcessor(bsp),
	)

	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(provider)

	return func() {
		ctx := context.Background()
		err := provider.Shutdown(ctx)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func instrumentedServer(handler http.HandlerFunc) *http.Server {
	omHandleFunc := func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		handler.ServeHTTP(w, r)

		ctx := r.Context()
		traceId := trace.SpanContextFromContext(ctx).TraceId.String()

		metricRequestLatency.(prometheus.ExemplarObserver).ObserveWithExemplar(
			time.Since(start).Seconds(), prometheus.Labels{"traceID": traceID},
		)

		logger.log("msg", "http request", "traceID", traceId, "path", f.URL.Path, "latency", time.Since(start))
	}

	otelHandler := otelhttp.NewHandler(http.HandlerFunc(omHandleFunc), "http")

	r := mux.NewRouter()
	r.Handle("/", otelHandler)
	r.Handle("/metrics", promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	return &http.Server{
		Handler: r,
		Addr:    "0.0.0.0:8000",
	}
}

func main() {
	godotenv.Load()

	cleanup := initTracer()
	defer cleanup()

	server := instrumentedServer(Hello)

	fmt.Println("listening...")
	server.ListenAndServe()
}

func Hello(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintln(w, "Hello world!")
}
