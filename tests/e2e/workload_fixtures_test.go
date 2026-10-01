//go:build e2e

package e2e

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Topograph is an NVIDIA-owned, public, multi-platform Alpine image. Keep
	// the release tag for provenance and pin the registry index digest
	// so this test always runs the inspected image.
	passiveFixtureImage = "ghcr.io/nvidia/topograph:v0.4.0@sha256:f227bf161464da4a23273a535914c98b33d26e5bf24bdf7f50159ab4b875b717"
)

// passiveFixturePodTemplate supplies the same small, unprivileged workload to
// the kubelet metrics DaemonSet and the component logs Deployment.
func passiveFixturePodTemplate(name string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": name}},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken:  new(false),
			TerminationGracePeriodSeconds: new(int64(1)),
			Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   new(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "memory",
				Image:           passiveFixtureImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sleep", "3600"},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:                new(int64(65532)),
					AllowPrivilegeEscalation: new(false),
					ReadOnlyRootFilesystem:   new(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
}
