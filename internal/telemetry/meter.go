// meter.go — configuração do OpenTelemetry Metrics.
//
// PARALELO DIRETO COM tracer.go:
//
//	tracer.go        meter.go
//	------------    -----------
//	TracerProvider  MeterProvider
//	SpanExporter    MetricExporter
//	WithBatcher     WithReader(PeriodicReader)
//
// Mesmo ambiente (stdout em dev, OTLP HTTP em prod), mesmo collector de destino.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// SetupMetrics inicializa o MeterProvider global.
// Retorna a função de shutdown — chame via defer no main() pra garantir flush
// dos dados em buffer antes do processo encerrar.
//
// A variável de ambiente OTEL_EXPORTER_OTLP_ENDPOINT é compartilhada entre
// traces e métricas — o mesmo endpoint serve pros dois (o collector aceita
// OTLP HTTP em /v1/traces, /v1/metrics, /v1/logs).
func SetupMetrics(ctx context.Context) (shutdown func(), err error) {
	exporter, err := newMetricExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating metric exporter: %w", err)
	}

	// Resource: mesmos atributos que o TracerProvider usa.
	// Importante: service.name e service.version precisam BATER entre traces e
	// métricas — é assim que o backend correlaciona os dois sinais do mesmo app.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("deployguard"),
			semconv.ServiceVersion("v0.1.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	// PeriodicReader: agrega e flusha métricas em intervalos regulares.
	// Default de 60s é razoável. Em dev pode baixar pra ver mais rápido.
	// Equivalente ao "scrape_interval" do Prometheus — mas é PUSH, não pull.
	reader := metric.NewPeriodicReader(exporter)

	mp := metric.NewMeterProvider(
		metric.WithReader(reader),
		metric.WithResource(res),
	)

	// Registra globalmente. A partir daqui, otel.Meter("nome") em qualquer
	// pacote pega esse provider sem injection manual.
	otel.SetMeterProvider(mp)

	shutdown = func() {
		// Shutdown chama flush + fecha readers + fecha exporters, na ordem certa.
		// Sem isso, as últimas métricas (as mais interessantes, geralmente) somem.
		if err := mp.Shutdown(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down meter provider: %v\n", err)
		}
	}

	return shutdown, nil
}

// newMetricExporter escolhe o exporter certo pro ambiente.
// Mesmo padrão do newExporter() em tracer.go.
func newMetricExporter(ctx context.Context) (metric.Exporter, error) {
	if os.Getenv("ENV") == "production" {
		// PROD: OTLP HTTP pro collector.
		// Configura via OTEL_EXPORTER_OTLP_ENDPOINT (env var padrão do OTEL).
		return otlpmetrichttp.New(ctx)
	}

	// DEV: stdout — você vê as métricas formatadas no terminal a cada
	// período do PeriodicReader. Ótimo pra validar que o instrumento tá
	// sendo incrementado, sem precisar subir Prometheus.
	return stdoutmetric.New(
		stdoutmetric.WithPrettyPrint(),
	)
}
