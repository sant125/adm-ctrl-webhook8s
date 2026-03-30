package rules

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

// BlockLatestTagRule rejeita containers que usam imagem com tag ":latest" ou sem tag.
//
// Por que isso importa:
// - ":latest" torna deploys não-determinísticos: o mesmo YAML pode subir imagens diferentes
// - Sem tag, o runtime assume "latest" implicitamente
// - Impede rastreabilidade (qual versão está rodando em prod?)
type BlockLatestTagRule struct{}

func (r *BlockLatestTagRule) Name() string {
	return "block-latest-tag"
}

func (r *BlockLatestTagRule) Evaluate(deploy *appsv1.Deployment) []Violation {
	var violations []Violation

	for i, container := range deploy.Spec.Template.Spec.Containers {
		if isLatestOrUntagged(container.Image) {
			violations = append(violations, Violation{
				Field: fmt.Sprintf("spec.template.spec.containers[%d].image", i),
				Message: fmt.Sprintf(
					"container %q uses image %q — tag ':latest' or untagged images are not allowed; use an explicit tag or digest",
					container.Name,
					container.Image,
				),
			})
		}
	}

	return violations
}

// isLatestOrUntagged retorna true se a imagem usa tag "latest" ou não tem tag.
//
// Casos que cobre:
//   - "nginx"           → sem tag → implica latest → bloqueado
//   - "nginx:latest"    → explícito latest → bloqueado
//   - "nginx:1.25"      → tag explícita → permitido
//   - "nginx@sha256:..." → digest → permitido (mais seguro que tag)
//   - "registry.io/org/nginx:v1.0" → tag explícita → permitido
func isLatestOrUntagged(image string) bool {
	// Remove o registry se presente (tudo antes da primeira "/")
	// mas só se contiver "." ou ":" (indica host:port ou domínio)
	// Exemplos: "gcr.io/project/image:tag" → precisamos achar a tag no final
	//
	// Estratégia simples e robusta: pegar o componente após o último ":"
	// mas cuidando que "registry:5000/image" não seja confundido com tag.

	// Se tem digest (@sha256:...), é permitido — não é latest.
	if strings.Contains(image, "@") {
		return false
	}

	// Pega a parte após a última "/"
	// "gcr.io/myproject/myimage:v1" → "myimage:v1"
	// "nginx:latest" → "nginx:latest"
	lastSlash := strings.LastIndex(image, "/")
	nameAndTag := image
	if lastSlash >= 0 {
		nameAndTag = image[lastSlash+1:]
	}

	// Se não tem ":" na parte nome:tag, não tem tag → implica latest.
	colonIdx := strings.LastIndex(nameAndTag, ":")
	if colonIdx < 0 {
		return true
	}

	// Extrai a tag após o último ":"
	tag := nameAndTag[colonIdx+1:]
	return tag == "" || tag == "latest"
}
