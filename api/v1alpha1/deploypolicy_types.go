// Package v1alpha1 define os tipos do CRD DeployPolicy.
// "v1alpha1" indica que a API ainda é experimental — convenção do Kubernetes.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion é o identificador canônico da API: grupo + versão.
	// É o que aparece no YAML do CRD: apiVersion: deployguard.io/v1alpha1
	GroupVersion = schema.GroupVersion{Group: "deployguard.io", Version: "v1alpha1"}

	// SchemeBuilder registra os tipos deste pacote.
	// O controller-runtime usa esse padrão para permitir que packages externos
	// adicionem seus tipos a um runtime.Scheme via AddToScheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme é a função exposta para uso externo (e.g. main.go).
	// Equivalente a: func AddToScheme(s *runtime.Scheme) error
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	// Registra DeployPolicy e DeployPolicyList no builder.
	// Quando AddToScheme for chamado, esses tipos serão adicionados ao scheme.
	SchemeBuilder.Register(&DeployPolicy{}, &DeployPolicyList{})
}

// DeployPolicySpec é o estado DESEJADO — o que o usuário declara no YAML.
// Cada campo mapeia para uma regra de validação ou mutação.
type DeployPolicySpec struct {
	// RequireResources rejeita Deployments sem resources.requests/limits definidos.
	// +optional
	RequireResources bool `json:"requireResources,omitempty"`

	// RequireReadinessProbe rejeita Deployments sem readinessProbe em todos os containers.
	// +optional
	RequireReadinessProbe bool `json:"requireReadinessProbe,omitempty"`

	// BlockLatestTag rejeita imagens com tag ":latest" ou sem tag (implica latest).
	// +optional
	BlockLatestTag bool `json:"blockLatestTag,omitempty"`
}

// DeployPolicyStatus é o estado OBSERVADO — o que o operator reporta de volta.
// Seguimos o padrão de Conditions do Kubernetes (mesmo padrão do Node, Pod, etc).
type DeployPolicyStatus struct {
	// Conditions lista as condições atuais do objeto.
	// A condição "Ready" indica se a policy foi reconciliada com sucesso.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// DeployPolicy é o CRD principal. Namespaced — cada namespace tem a sua policy.
type DeployPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeployPolicySpec   `json:"spec,omitempty"`
	Status DeployPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DeployPolicyList é necessário para o controller-runtime listar recursos.
type DeployPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeployPolicy `json:"items"`
}
