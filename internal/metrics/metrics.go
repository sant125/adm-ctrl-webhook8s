// Package metrics define os instrumentos OpenTelemetry usados pelo DeployGuard.
//
// ==========================================================================
// POR QUE OTEL METRICS (E NÃO PROMETHEUS CLIENT DIRETO)?
// ==========================================================================
//
// O projeto já usa OTEL pra traces (ver internal/telemetry). A mesma filosofia
// se aplica a métricas: o APP só fala OTLP, e o COLLECTOR decide pra onde vai.
//
// Vantagens:
//  1. Vendor-neutral — hoje é Tempo+Prometheus, amanhã pode ser Cloud Monitoring,
//     Datadog, New Relic… sem mexer em NENHUMA linha de código Go. É só trocar
//     o exporter no collector.yaml.
//  2. Uma biblioteca só — OTEL SDK instrumenta traces, métricas e logs. Menos
//     dependência, menos CVE pra cuidar, menos mental model.
//  3. Correlação de sinais — OTEL suporta exemplars: métrica com ponteiro pro
//     trace que causou aquela observação. "Por que p99 subiu às 14h?" → clica,
//     cai num trace real. Difícil de fazer com stacks separadas.
//
// ==========================================================================
// API OTEL EM 4 CONCEITOS
// ==========================================================================
//
//	Meter       → "origem" da medição, identificada por nome (tipo o Tracer).
//	               otel.Meter("deployguard/webhook") retorna o meter global.
//
//	Counter     → valor monotônico crescente. Add(ctx, 1, attrs).
//	Histogram   → distribuição (pra p50/p95/p99). Record(ctx, valor, attrs).
//	UpDownCounter → valor que pode subir e descer (gauge-like).
//
//	Attributes  → equivalente aos "labels" do Prometheus, mas tipados
//	              (attribute.String, attribute.Int, etc).
//
// ==========================================================================
// CARDINALIDADE — A REGRA DE OURO QUE QUEBRA PROJETOS
// ==========================================================================
//
// Cada combinação única de atributos = 1 série temporal. Série temporal custa
// memória no backend (Prometheus guarda em RAM, Cloud Monitoring cobra por série).
//
// OK (valores finitos): decision={allow,deny,error}, rule={3-5 regras}, namespace
// RUIM: deployment_name, image_tag, user_id, trace_id — explodem sem limite.
//
// Pra observar UM caso específico, use exemplars (trace ID dentro do histograma)
// ou logs estruturados. Métrica é pra AGREGADO.
package metrics

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// meterName identifica a origem dessas métricas nos traces/logs de debug.
// Convenção OTEL: "<componente>" ou "<domínio>/<componente>".
const meterName = "deployguard"

// Variáveis globais com os instrumentos registrados.
// Preenchidas por Init() uma vez no startup; referenciadas pelo código de negócio
// via metrics.AdmissionRequests.Add(...).
var (
	// AdmissionRequests — total de requests de admissão processados.
	// Atributos: namespace, operation (CREATE/UPDATE), decision (allow/deny/error).
	//
	// Uso típico em PromQL (após collector exportar pra Prom):
	//   sum(rate(deployguard_admission_requests_total{decision="deny"}[5m])) by (namespace)
	AdmissionRequests metric.Int64Counter

	// Violations — total de violações individuais detectadas.
	// Um request pode gerar N violations (o evaluator roda todas as regras).
	// Atributos: namespace, rule.
	//
	// topk(3, sum by (rule) (rate(deployguard_policy_violations_total[7d])))
	//   → top 3 regras mais violadas na semana.
	Violations metric.Int64Counter

	// AdmissionDuration — latência do processamento de cada admission.
	// Histograma: OTEL gera buckets automáticos ou fixos (veja View abaixo).
	// Atributo: decision.
	//
	// Usado pra p95/p99 e pra monitorar proximidade do timeout do API server
	// (webhookconfig.yaml timeoutSeconds: 5).
	AdmissionDuration metric.Float64Histogram
)

// Init constrói os instrumentos e guarda as referências globais.
//
// Deve ser chamado UMA vez no startup, DEPOIS do MeterProvider estar registrado
// (ou seja, depois de telemetry.SetupMetrics). Chamar antes faz os instrumentos
// usarem o MeterProvider no-op — eles gravam em /dev/null silenciosamente.
//
// Por que função explícita em vez de init()?
//   - init() roda em ordem imprevisível entre pacotes. Se nosso init() rodar antes
//     do telemetry registrar o provider, os instrumentos pegam o no-op global.
//   - Com Init() explícito chamado do main.go, o main controla a ordem.
func Init() error {
	meter := otel.Meter(meterName)

	var err error

	AdmissionRequests, err = meter.Int64Counter(
		"deployguard.admission.requests",
		metric.WithDescription("Total de requests de admission processados, por decisão."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("creating admission requests counter: %w", err)
	}

	Violations, err = meter.Int64Counter(
		"deployguard.policy.violations",
		metric.WithDescription("Total de violações de policy detectadas, por regra."),
		metric.WithUnit("{violation}"),
	)
	if err != nil {
		return fmt.Errorf("creating violations counter: %w", err)
	}

	AdmissionDuration, err = meter.Float64Histogram(
		"deployguard.admission.duration",
		metric.WithDescription("Latência do processamento de admission."),
		metric.WithUnit("s"),
		// Buckets explícitos focados em sub-segundo. O timeout do API server pro
		// webhook é 5s (ver webhookconfig.yaml), então qualquer coisa acima disso
		// já é "webhook morreu, API server cancelou".
		metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5),
	)
	if err != nil {
		return fmt.Errorf("creating admission duration histogram: %w", err)
	}

	return nil
}
