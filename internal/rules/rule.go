// Package rules define a interface Rule e o tipo Violation.
// Cada regra de validação é um arquivo separado que implementa essa interface.
//
// PATTERN: Open/Closed Principle
// - Aberto para extensão: adicionar nova regra = criar novo arquivo implementando Rule
// - Fechado para modificação: o evaluator e o handler não mudam quando nova regra é adicionada
package rules

import appsv1 "k8s.io/api/apps/v1"

// Violation representa uma única infração encontrada no Deployment.
// Ter um tipo dedicado (em vez de só string) permite filtrar, agrupar e serializar violations.
type Violation struct {
	// Field é o caminho JSONPath do campo problemático.
	// Exemplo: "spec.containers[0].resources.requests"
	Field string

	// Message é a descrição humana do problema.
	// Exemplo: "resources.requests must be set for container 'nginx'"
	Message string
}

// Rule é a interface que toda regra de validação deve implementar.
// Mantendo a interface pequena (2 métodos) seguimos o princípio de Interface Segregation.
type Rule interface {
	// Name retorna o identificador da regra.
	// Usado em logs e como chave em métricas.
	Name() string

	// Evaluate inspeciona o Deployment e retorna todas as violações encontradas.
	// Retornar slice vazio (ou nil) significa que o Deployment passou na regra.
	// IMPORTANTE: Evaluate NÃO modifica o Deployment — mutação é responsabilidade do mutator.
	Evaluate(deploy *appsv1.Deployment) []Violation
}
