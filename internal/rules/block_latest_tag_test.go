package rules

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestBlockLatestTagRule(t *testing.T) {
	tests := []struct {
		name      string
		image     string
		wantBlock bool // true = esperamos violation
	}{
		// casos bloqueados
		{"tag latest explícita", "nginx:latest", true},
		{"sem tag — implica latest", "nginx", true},
		{"registry sem tag", "gcr.io/project/image", true},
		{"tag vazia após :", "nginx:", true},

		// casos permitidos
		{"tag semver", "nginx:1.25.3", false},
		{"tag com registry", "gcr.io/project/image:v1.0", false},
		{"digest sha256", "nginx@sha256:abc123", false},
		{"tag de release candidate", "nginx:1.25.3-alpine", false},
	}

	rule := &BlockLatestTagRule{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deploy := deployWithImage(tc.image)
			violations := rule.Evaluate(deploy)

			hasViolation := len(violations) > 0
			if hasViolation != tc.wantBlock {
				t.Errorf("image %q: got violation=%v, want violation=%v",
					tc.image, hasViolation, tc.wantBlock)
			}
		})
	}
}

func deployWithImage(image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: image},
					},
				},
			},
		},
	}
}
