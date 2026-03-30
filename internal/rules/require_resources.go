package rules

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// RequireResourcesRule valida que todos os containers têm resources.requests definidos.
// Sem requests, o scheduler do Kubernetes não consegue fazer bin packing correto,
// podendo causar OOMKill ou starvation de CPU em outros pods no mesmo node.
type RequireResourcesRule struct{}

func (r *RequireResourcesRule) Name() string {
	return "require-resources"
}

func (r *RequireResourcesRule) Evaluate(deploy *appsv1.Deployment) []Violation {
	var violations []Violation

	// Iteramos por index para poder referenciar "containers[i]" na mensagem de erro.
	// Isso ajuda o dev a saber exatamente qual container está com problema.
	for i, container := range deploy.Spec.Template.Spec.Containers {
		violations = append(violations, checkContainer(i, container)...)
	}

	return violations
}

// checkContainer extrai a lógica de validação de um único container.
// Funções pequenas e focadas são mais fáceis de testar unitariamente.
func checkContainer(index int, c corev1.Container) []Violation {
	var violations []Violation

	if c.Resources.Requests == nil {
		violations = append(violations, Violation{
			Field:   fmt.Sprintf("spec.template.spec.containers[%d].resources.requests", index),
			Message: fmt.Sprintf("container %q must have resources.requests defined", c.Name),
		})
		// Se requests é nil, não adianta checar campos individuais — retorna cedo.
		return violations
	}

	// Verifica CPU e memória separadamente para dar mensagem de erro específica.
	if _, ok := c.Resources.Requests[corev1.ResourceCPU]; !ok {
		violations = append(violations, Violation{
			Field:   fmt.Sprintf("spec.template.spec.containers[%d].resources.requests.cpu", index),
			Message: fmt.Sprintf("container %q must have resources.requests.cpu defined", c.Name),
		})
	}

	if _, ok := c.Resources.Requests[corev1.ResourceMemory]; !ok {
		violations = append(violations, Violation{
			Field:   fmt.Sprintf("spec.template.spec.containers[%d].resources.requests.memory", index),
			Message: fmt.Sprintf("container %q must have resources.requests.memory defined", c.Name),
		})
	}

	return violations
}
