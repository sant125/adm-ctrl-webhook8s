package rules

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestRequireReadinessProbeRule(t *testing.T) {
	tests := []struct {
		name      string
		deploy    *appsv1.Deployment
		wantCount int
	}{
		{
			name:      "container com httpGet probe — passa",
			deploy:    deployWithHTTPProbe(),
			wantCount: 0,
		},
		{
			name:      "container com tcpSocket probe — passa",
			deploy:    deployWithTCPProbe(),
			wantCount: 0,
		},
		{
			name:      "container sem probe — 1 violation",
			deploy:    deployWithoutProbe(),
			wantCount: 1,
		},
		{
			// dois containers sem probe → 2 violations, um por container
			name:      "dois containers sem probe — 2 violations",
			deploy:    deployTwoContainersNoProbe(),
			wantCount: 2,
		},
		{
			// um com probe, um sem → 1 violation só do segundo
			name:      "um com probe, um sem — 1 violation",
			deploy:    deployMixedProbes(),
			wantCount: 1,
		},
	}

	rule := &RequireReadinessProbeRule{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rule.Evaluate(tc.deploy)
			if len(got) != tc.wantCount {
				t.Errorf("Evaluate() returned %d violation(s), want %d\nviolations: %+v",
					len(got), tc.wantCount, got)
			}
		})
	}
}

func deployWithHTTPProbe() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func deployWithTCPProbe() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt(8080),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func deployWithoutProbe() *appsv1.Deployment {
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

func deployTwoContainersNoProbe() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.25"},
						{Name: "sidecar", Image: "busybox:1.36"},
					},
				},
			},
		},
	}
}

func deployMixedProbes() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.25",
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.FromInt(80),
									},
								},
							},
						},
						{
							// sidecar sem probe — deve gerar violation
							Name:  "sidecar",
							Image: "busybox:1.36",
						},
					},
				},
			},
		},
	}
}
