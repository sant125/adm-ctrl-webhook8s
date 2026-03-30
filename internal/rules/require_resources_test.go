package rules

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestRequireResourcesRule(t *testing.T) {
	tests := []struct {
		name      string
		deploy    *appsv1.Deployment
		wantCount int // quantas violations esperamos
	}{
		{
			name:      "container com cpu e memory definidos — passa",
			deploy:    deployWithResources("100m", "128Mi"),
			wantCount: 0,
		},
		{
			name:      "container sem resources — 1 violation",
			deploy:    deployWithoutResources(),
			wantCount: 1,
		},
		{
			name:      "container com requests nil — 1 violation",
			deploy:    deployWithNilRequests(),
			wantCount: 1,
		},
		{
			name:      "container com cpu mas sem memory — 1 violation",
			deploy:    deployWithOnlyCPU("100m"),
			wantCount: 1,
		},
		{
			name:      "container com memory mas sem cpu — 1 violation",
			deploy:    deployWithOnlyMemory("128Mi"),
			wantCount: 1,
		},
		{
			// dois containers: um válido, um sem resources → 1 violation
			name:      "dois containers, um inválido — 1 violation",
			deploy:    deployWithTwoContainers(),
			wantCount: 1,
		},
	}

	rule := &RequireResourcesRule{}

	for _, tc := range tests {
		// t.Run cria um subtest com nome próprio.
		// Se falhar, o output mostra exatamente qual caso quebrou.
		// Rode com: go test ./internal/rules/... -v -run TestRequireResourcesRule
		t.Run(tc.name, func(t *testing.T) {
			got := rule.Evaluate(tc.deploy)
			if len(got) != tc.wantCount {
				t.Errorf("Evaluate() returned %d violation(s), want %d\nviolations: %+v",
					len(got), tc.wantCount, got)
			}
		})
	}
}

// helpers — constroem Deployments mínimos para cada cenário.
// Ficam no mesmo pacote (rules) para acesso direto, sem exportar.

func deployWithResources(cpu, memory string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(cpu),
									corev1.ResourceMemory: resource.MustParse(memory),
								},
							},
						},
					},
				},
			},
		},
	}
}

func deployWithoutResources() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.25"},
					},
				},
			},
		},
	}
}

func deployWithNilRequests() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							Resources: corev1.ResourceRequirements{
								Requests: nil, // explicitamente nil
							},
						},
					},
				},
			},
		},
	}
}

func deployWithOnlyCPU(cpu string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse(cpu),
									// sem memory
								},
							},
						},
					},
				},
			},
		},
	}
}

func deployWithOnlyMemory(memory string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse(memory),
									// sem cpu
								},
							},
						},
					},
				},
			},
		},
	}
}

func deployWithTwoContainers() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
						{
							// segundo container sem resources — deve gerar 1 violation
							Name:  "sidecar",
							Image: "busybox:1.36",
						},
					},
				},
			},
		},
	}
}
