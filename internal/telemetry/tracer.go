// Package telemetry configura o OpenTelemetry para o deployguard.
//
// CONCEITO CENTRAL:
// O OTEL separa "o que instrumentar" de "pra onde exportar".
// Seu código de negócio só usa otel.Tracer() — não sabe nem se importa com o destino.
// O destino é configurado aqui, uma vez, no setup.
//
// DOIS MODOS:
// - dev  (ENV != "production"): exporta pro stdout — você vê os spans no terminal
// - prod (ENV == "production"): exporta via OTLP pro OTEL Collector
//   O collector roda no cluster e exporta pro Cloud Trace via Workload Identity.
//   O app não sabe nem se importa com o destino final — só fala OTLP.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// Setup inicializa o TracerProvider e o registra globalmente.
// Retorna uma função de shutdown que deve ser chamada no encerramento do processo
// para garantir que todos os spans em buffer sejam exportados antes de sair.
//
// Uso no main.go:
//
//	shutdown, err := telemetry.Setup(ctx)
//	if err != nil { ... }
//	defer shutdown()
func Setup(ctx context.Context) (shutdown func(), err error) {
	exporter, err := newExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating trace exporter: %w", err)
	}

	// resource descreve o serviço que está sendo instrumentado.
	// Esses atributos aparecem em todos os spans — identificam a origem.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("deployguard"),
			semconv.ServiceVersion("v0.1.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	// TracerProvider é o objeto central do OTEL.
	// Ele gerencia o ciclo de vida dos spans e os envia pro exporter.
	//
	// WithBatcher: agrupa spans em batches antes de exportar — mais eficiente
	// que exportar um por um, especialmente em prod com muitas requests.
	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(exporter),
		tracesdk.WithResource(res),
	)

	// Registra globalmente — a partir daqui otel.Tracer("nome") funciona
	// em qualquer lugar do código sem precisar passar o tp por injeção.
	otel.SetTracerProvider(tp)

	shutdown = func() {
		// Flush: garante que spans em buffer sejam enviados antes do processo morrer.
		// Importante no graceful shutdown — sem isso você perde os últimos spans.
		if err := tp.Shutdown(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down tracer provider: %v\n", err)
		}
	}

	return shutdown, nil
}

// newExporter cria o exporter adequado para o ambiente.
//
// Por que separar em função?
// Facilita trocar o destino sem mexer no TracerProvider.
// Em testes você pode injetar um exporter de teste.
func newExporter(ctx context.Context) (tracesdk.SpanExporter, error) {
	if os.Getenv("ENV") == "production" {
		// PROD: exporta via OTLP HTTP pro OTEL Collector rodando no cluster.
		// O endpoint é configurado via OTEL_EXPORTER_OTLP_ENDPOINT.
		// O collector recebe OTLP e exporta pro Cloud Trace via Workload Identity —
		// o app não precisa saber nada sobre autenticação GCP.
		return otlptracehttp.New(ctx)
	}

	// DEV: imprime os spans no stdout em formato legível.
	// Você vê exatamente o que seria enviado pro Cloud Trace,
	// sem precisar de nenhuma infraestrutura rodando.
	return stdouttrace.New(
		stdouttrace.WithPrettyPrint(),
	)
}
