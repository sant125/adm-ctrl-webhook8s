package rules

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
)

// RequireReadinessProbeRule valida que todos os containers têm readinessProbe configurado.
//
// Por que isso importa:
// - Sem readinessProbe, o Kubernetes marca o pod como Ready imediatamente após o container iniciar
// - Isso faz o Service mandar tráfego para o pod ANTES da aplicação estar pronta
// - Resultado: erros 502/503 durante deploys rolling update
type RequireReadinessProbeRule struct{}

func (r *RequireReadinessProbeRule) Name() string {
	return "require-readiness-probe"
}

func (r *RequireReadinessProbeRule) Evaluate(deploy *appsv1.Deployment) []Violation {
	var violations []Violation

	for i, container := range deploy.Spec.Template.Spec.Containers {
		// ReadinessProbe nil significa "não configurado"
		if container.ReadinessProbe == nil {
			violations = append(violations, Violation{
				Field: fmt.Sprintf("spec.template.spec.containers[%d].readinessProbe", i),
				Message: fmt.Sprintf(
					"container %q must have a readinessProbe defined to prevent traffic during startup",
					container.Name,
				),
			})
		}
	}

	return violations
}
