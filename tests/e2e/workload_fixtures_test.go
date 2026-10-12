// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/wait"
)

const (
	// Pin the public, multi-platform fixture image by digest.
	passiveFixtureImage = "ghcr.io/nvidia/topograph:v0.4.0@sha256:f227bf161464da4a23273a535914c98b33d26e5bf24bdf7f50159ab4b875b717"
)

// passiveFixturePodTemplate supplies a small, unprivileged Alpine workload
// for e2e fixtures.
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

// waitFixturePodPhase waits for a fixture to run or complete and includes the
// last pod status when startup or completion times out.
func waitFixturePodPhase(ctx context.Context, client klient.Client, ns, name string, phase corev1.PodPhase) error {
	var status corev1.PodStatus
	err := wait.For(func(ctx context.Context) (bool, error) {
		var pod corev1.Pod
		err := client.Resources(ns).Get(ctx, name, ns, &pod)
		if err != nil {
			return false, err
		}
		status = pod.Status
		if status.Phase == corev1.PodFailed || (status.Phase == corev1.PodSucceeded && phase != corev1.PodSucceeded) {
			return false, fmt.Errorf("fixture %s/%s stopped: %+v", ns, name, status)
		}
		return status.Phase == phase, nil
	}, wait.WithContext(ctx), wait.WithTimeout(0), wait.WithInterval(time.Second), wait.WithImmediate())
	if err != nil {
		return fmt.Errorf("waiting for fixture %s/%s to be %s: %w; last status: %+v", ns, name, phase, err, status)
	}
	return nil
}
