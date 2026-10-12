// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestKubeStateMetrics checks recent fixture metrics from every default KSM
// collection group, including node-local and unscheduled pods. The groups must
// watch the fixture namespace. Its unbound PVC avoids provisioning or using
// existing volumes. Cleanup requests deletion of the namespace and its objects.
func TestKubeStateMetrics(t *testing.T) {
	releaseNamespace := testenv.EnvConf().Namespace()
	type metricCheck struct{ name, metric string }
	var checks []metricCheck
	var proxyClient coreclient.CoreV1Interface
	ns := envconf.RandomName("nktp-e2e-ksm", 48)
	f := features.New("Kube-state metrics").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var err error
			proxyClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			setupCtx, err = envfuncs.CreateNamespace(ns)(setupCtx, c)
			require.NoError(t, err, "creating fixture namespace")
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(setupCtx), 30*time.Second)
				defer cancel()
				_, err := envfuncs.DeleteNamespace(ns)(cleanupCtx, c)
				assert.NoError(t, err, "deleting fixture namespace %s", ns)
			})
			const name = "fixture"

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string]string{"fixture": "true"},
			}
			require.NoError(t, c.Client().Resources(ns).Create(setupCtx, configMap))
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: corev1.ServiceSpec{Selector: map[string]string{"app.kubernetes.io/name": name},
					Ports: []corev1.ServicePort{{Name: "fixture", Port: 8080}}},
			}
			require.NoError(t, c.Client().Resources(ns).Create(setupCtx, service))
			claim := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName:       envconf.RandomName("nktp-e2e-ksm", 40),
					StorageClassName: new(""), AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Mi")}},
				},
			}
			var volume corev1.PersistentVolume
			for {
				err = c.Client().Resources().Get(setupCtx, claim.Spec.VolumeName, "", &volume)
				if apierrors.IsNotFound(err) {
					break
				}
				require.NoError(t, err, "checking whether PV %s exists", claim.Spec.VolumeName)
				claim.Spec.VolumeName = envconf.RandomName("nktp-e2e-ksm", 40)
			}
			require.NoError(t, c.Client().Resources(ns).Create(setupCtx, claim))
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{Replicas: new(int32(1)),
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": name}},
					Template: passiveFixturePodTemplate(name)},
			}
			require.NoError(t, c.Client().Resources(ns).Create(setupCtx, deployment))

			for _, tc := range []struct{ group, metric, labels string }{
				{"cluster", "kube_namespace_status_phase", `phase="Active"`},
				{"configuration", "kube_configmap_info", `configmap="fixture"`},
				{"networking", "kube_service_info", `service="fixture"`},
				{"storage", "kube_persistentvolumeclaim_info", `persistentvolumeclaim="fixture"`},
				{"workloads", "kube_deployment_spec_replicas", `deployment="fixture"`},
			} {
				checks = append(checks, metricCheck{tc.group, fmt.Sprintf(`%s{namespace=%q,job=%q,nktp_metrics_pool="large-target",%s}`,
					tc.metric, ns, releaseNamespace+"/nktp-ksm-"+tc.group, tc.labels)})
			}

			var nodes corev1.NodeList
			err = c.Client().Resources().List(setupCtx, &nodes)
			require.NoError(t, err)
			require.NotEmpty(t, nodes.Items)
			for i, node := range nodes.Items {
				template := passiveFixturePodTemplate(name)
				template.Spec.NodeName = node.Name
				pod, _ := funcs.CreateFixturePod(setupCtx, t, c.Client(), ns, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i), Labels: template.Labels}, Spec: template.Spec,
				})
				require.NoError(t, waitFixturePodPhase(setupCtx, c.Client(), ns, pod.Name, corev1.PodRunning))
				checks = append(checks, metricCheck{"pods/" + node.Name,
					fmt.Sprintf(`kube_pod_info{namespace=%q,pod=%q,uid=%q,node=%q,job=%q,nktp_metrics_pool=""}`,
						ns, pod.Name, pod.UID, node.Name, releaseNamespace+"/nktp-ksm-pods")})
			}
			template := passiveFixturePodTemplate(name)
			template.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "nktp.nvidia.io/e2e"}}
			unscheduled, _ := funcs.CreateFixturePod(setupCtx, t, c.Client(), ns, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "unscheduled", Labels: template.Labels}, Spec: template.Spec,
			})
			checks = append(checks, metricCheck{"unscheduled-pods",
				fmt.Sprintf(`kube_pod_info{namespace=%q,pod=%q,uid=%q,node="",job=%q,nktp_metrics_pool="large-target"}`,
					ns, unscheduled.Name, unscheduled.UID, releaseNamespace+"/nktp-ksm-unscheduled-pods")})

			return ctx
		}).
		Assess("Every collection group delivers recent fixture metrics", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			found := make([]bool, len(checks))
			queryErrors := make([]error, len(checks))
			err := wait.For(func(ctx context.Context) (bool, error) {
				complete := true
				for i, check := range checks {
					// Every selected metric is a gauge whose fixture value must be 1.
					expression := fmt.Sprintf(`(%s == 1) and (timestamp(%s) > time() - %d)`,
						check.metric, check.metric, int(componentMetricMaxAge.Seconds()))
					values, queryErr := prometheusMetricLabelValues(ctx, proxyClient.Services(releaseNamespace), expression, "namespace")
					queryErrors[i] = queryErr
					found[i] = queryErr == nil && coversValues(values, []string{ns})
					complete = complete && found[i]
				}
				return complete, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			for i, check := range checks {
				if assert.NoError(t, queryErrors[i], "querying %s", check.name) {
					assert.True(t, found[i], "KSM fixture metric missing, stale, or incorrect: %s", check.metric)
				}
			}
			assert.NoError(t, err, "waiting for KSM metrics in LGTM")
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
