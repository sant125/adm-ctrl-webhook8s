// Package policy contém o Evaluator — responsável por orquestrar as regras.
// Ele lê a DeployPolicy do namespace e monta a lista de regras ativas.
package policy

import (
	"context"

	"github.com/santzin/deployguard/api/v1alpha1"
	"github.com/santzin/deployguard/internal/metrics"
	"github.com/santzin/deployguard/internal/rules"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	appsv1 "k8s.io/api/apps/v1"
)

// tracer é o tracer deste pacote.
// otel.Tracer() retorna o tracer registrado globalmente no main.go via telemetry.Setup().
// Nome por convenção: "módulo/pacote" — aparece nos spans no Jaeger/Tempo.
var tracer = otel.Tracer("deployguard/policy")

// Evaluator agrega as regras ativas e as executa contra um Deployment.
//
// PATTERN: Strategy
// As regras são estratégias intercambiáveis. O Evaluator não sabe COMO cada
// regra funciona — só sabe que todas implementam a interface Rule.
type Evaluator struct {
	activeRules []rules.Rule
}

// New constrói um Evaluator com as regras habilitadas na DeployPolicy.
// Regras desabilitadas simplesmente não são adicionadas — zero custo em runtime.
//
// Se policy for nil (namespace sem policy), retorna Evaluator sem regras.
// Isso implementa o comportamento "opt-in": namespaces sem policy não são restringidos.
func New(policy *v1alpha1.DeployPolicy) *Evaluator {
	if policy == nil {
		return &Evaluator{}
	}

	var active []rules.Rule

	// Cada bloco if adiciona a regra correspondente apenas se habilitada.
	// A ordem importa: regras são avaliadas na ordem de adição.
	// Colocamos as mais comuns primeiro para mensagens de erro mais úteis.
	if policy.Spec.RequireResources {
		active = append(active, &rules.RequireResourcesRule{})
	}

	if policy.Spec.BlockLatestTag {
		active = append(active, &rules.BlockLatestTagRule{})
	}

	if policy.Spec.RequireReadinessProbe {
		active = append(active, &rules.RequireReadinessProbeRule{})
	}

	return &Evaluator{activeRules: active}
}

// Evaluate roda todas as regras ativas contra o Deployment e retorna TODAS as violações.
//
// Por que retornar todas e não parar na primeira?
// - Melhor DX: o dev recebe uma lista completa de problemas para corrigir de uma vez
// - Comportamento análogo a compiladores modernos (Go, Rust) que mostram todos os erros
func (e *Evaluator) Evaluate(ctx context.Context, deploy *appsv1.Deployment) []rules.Violation {
	// Span pai: representa a avaliação completa de todas as regras.
	// O ctx já carrega o span do handleValidate — esse vira filho dele.
	ctx, span := tracer.Start(ctx, "policy.Evaluate")
	defer span.End()

	var allViolations []rules.Violation

	for _, rule := range e.activeRules {
		// Span filho por regra: você vê no trace exatamente qual regra demorou.
		_, ruleSpan := tracer.Start(ctx, "rule/"+rule.Name())

		violations := rule.Evaluate(deploy)

		// Atributos enriquecem o span com contexto de negócio.
		// Você filtra no Jaeger por "violations > 0" pra ver só os que falharam.
		ruleSpan.SetAttributes(
			attribute.Int("violations", len(violations)),
			attribute.Bool("passed", len(violations) == 0),
		)
		ruleSpan.End()

		// Métrica: incrementa UMA vez POR VIOLAÇÃO encontrada pela regra.
		// Isso permite responder "qual regra foi mais violada na última semana?"
		// agregando por label `rule`. Labels: namespace do deployment + nome da regra.
		if n := len(violations); n > 0 {
			metrics.Violations.Add(ctx, int64(n), metric.WithAttributes(
				attribute.String("namespace", deploy.Namespace),
				attribute.String("rule", rule.Name()),
			))
		}

		allViolations = append(allViolations, violations...)
	}

	span.SetAttributes(attribute.Int("total_violations", len(allViolations)))
	return allViolations
}

// HasRules retorna true se há pelo menos uma regra ativa.
// Usado pelo handler para decidir se vale fazer o lookup da policy.
func (e *Evaluator) HasRules() bool {
	return len(e.activeRules) > 0
}
