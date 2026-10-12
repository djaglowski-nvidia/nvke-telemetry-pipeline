// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package funcs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
)

const workloadWaitTimeout = 15 * time.Minute

type DeploymentWaitOptions struct {
	Name, Namespace string
	Client          klient.Client
	Rollout         bool
	ReplicaCount    *int32
}

// WaitForDeployment waits for availability or a full rollout when Rollout is
// set, optionally checking ReplicaCount. The wait is capped at 15 minutes.
func WaitForDeployment(ctx context.Context, opts DeploymentWaitOptions) error {
	if opts.Name == "" {
		return errors.New("deployment wait: name is required")
	}
	if opts.Namespace == "" {
		return errors.New("deployment wait: namespace is required")
	}
	if opts.Client == nil {
		return errors.New("deployment wait: client is required")
	}
	if opts.ReplicaCount != nil && *opts.ReplicaCount < 0 {
		return errors.New("deployment wait: replica count must be non-negative")
	}

	expected := "current generation observed and Available=True"
	if opts.Rollout {
		expected = "current generation observed, all desired replicas updated and available, no old replicas"
	}
	if opts.ReplicaCount != nil {
		expected += fmt.Sprintf(", desired replicas=%d", *opts.ReplicaCount)
	}
	last := "not observed"
	resources := opts.Client.Resources(opts.Namespace)
	err := wait.For(func(ctx context.Context) (bool, error) {
		var deployment appsv1.Deployment
		if err := resources.Get(ctx, opts.Name, opts.Namespace, &deployment); err != nil {
			if apierrors.IsNotFound(err) {
				last = "not found"
				return false, nil
			}
			return false, err
		}
		want := int32(1)
		if deployment.Spec.Replicas != nil {
			want = *deployment.Spec.Replicas
		}
		available := false
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
				available = true
				break
			}
		}
		status := deployment.Status
		last = fmt.Sprintf("generation=%d observed=%d desired=%d replicas=%d updated=%d ready=%d available=%d Available=%t",
			deployment.Generation, status.ObservedGeneration, want, status.Replicas,
			status.UpdatedReplicas, status.ReadyReplicas, status.AvailableReplicas, available)
		if opts.ReplicaCount != nil && want != *opts.ReplicaCount {
			return false, nil
		}
		if opts.Rollout {
			return status.ObservedGeneration >= deployment.Generation &&
				status.UpdatedReplicas == want && status.Replicas == want &&
				status.AvailableReplicas == want, nil
		}
		return status.ObservedGeneration >= deployment.Generation && available, nil
	}, wait.WithInterval(5*time.Second), wait.WithContext(ctx), wait.WithTimeout(workloadWaitTimeout), wait.WithImmediate())
	if err != nil {
		return fmt.Errorf("waiting for Deployment %s/%s: expected %s; last observed: %s: %w",
			opts.Namespace, opts.Name, expected, last, err)
	}
	return nil
}

type DaemonSetWaitOptions struct {
	Name, Namespace string
	Client          klient.Client
	NodeCount       *int32
}

// WaitForDaemonSet waits for all scheduled replicas to be updated, ready, and
// available, optionally checking NodeCount. The wait is capped at 15 minutes.
func WaitForDaemonSet(ctx context.Context, opts DaemonSetWaitOptions) error {
	if opts.Name == "" {
		return errors.New("daemonset wait: name is required")
	}
	if opts.Namespace == "" {
		return errors.New("daemonset wait: namespace is required")
	}
	if opts.Client == nil {
		return errors.New("daemonset wait: client is required")
	}
	if opts.NodeCount != nil && *opts.NodeCount < 0 {
		return errors.New("daemonset wait: node count must be non-negative")
	}

	expected := "current generation observed, all desired replicas updated, ready and available"
	if opts.NodeCount != nil {
		expected += fmt.Sprintf(", scheduled nodes=%d", *opts.NodeCount)
	}
	last := "not observed"
	resources := opts.Client.Resources(opts.Namespace)
	err := wait.For(func(ctx context.Context) (bool, error) {
		var ds appsv1.DaemonSet
		if err := resources.Get(ctx, opts.Name, opts.Namespace, &ds); err != nil {
			if apierrors.IsNotFound(err) {
				last = "not found"
				return false, nil
			}
			return false, err
		}
		status := ds.Status
		want := status.DesiredNumberScheduled
		last = fmt.Sprintf("generation=%d observed=%d desired=%d scheduled=%d updated=%d ready=%d available=%d",
			ds.Generation, status.ObservedGeneration, want, status.CurrentNumberScheduled,
			status.UpdatedNumberScheduled, status.NumberReady, status.NumberAvailable)
		if opts.NodeCount != nil && want != *opts.NodeCount {
			return false, nil
		}
		return status.ObservedGeneration >= ds.Generation &&
			status.UpdatedNumberScheduled == want &&
			status.NumberReady == want &&
			status.NumberAvailable == want, nil
	}, wait.WithInterval(5*time.Second), wait.WithContext(ctx), wait.WithTimeout(workloadWaitTimeout), wait.WithImmediate())
	if err != nil {
		return fmt.Errorf("waiting for DaemonSet %s/%s: expected %s; last observed: %s: %w",
			opts.Namespace, opts.Name, expected, last, err)
	}
	return nil
}

type StatefulSetWaitOptions struct {
	Name, Namespace string
	Client          klient.Client
}

// WaitForStatefulSet waits up to 15 minutes for the current generation's desired
// replicas to be ready; it does not check upgrade rollout completion.
func WaitForStatefulSet(ctx context.Context, opts StatefulSetWaitOptions) error {
	if opts.Name == "" {
		return errors.New("statefulset wait: name is required")
	}
	if opts.Namespace == "" {
		return errors.New("statefulset wait: namespace is required")
	}
	if opts.Client == nil {
		return errors.New("statefulset wait: client is required")
	}

	last := "not observed"
	resources := opts.Client.Resources(opts.Namespace)
	err := wait.For(func(ctx context.Context) (bool, error) {
		var sts appsv1.StatefulSet
		if err := resources.Get(ctx, opts.Name, opts.Namespace, &sts); err != nil {
			if apierrors.IsNotFound(err) {
				last = "not found"
				return false, nil
			}
			return false, err
		}
		want := int32(1)
		if sts.Spec.Replicas != nil {
			want = *sts.Spec.Replicas
		}
		last = fmt.Sprintf("generation=%d observed=%d desired=%d replicas=%d ready=%d",
			sts.Generation, sts.Status.ObservedGeneration, want, sts.Status.Replicas, sts.Status.ReadyReplicas)
		return sts.Status.ObservedGeneration >= sts.Generation && sts.Status.ReadyReplicas == want, nil
	}, wait.WithInterval(5*time.Second), wait.WithContext(ctx), wait.WithTimeout(workloadWaitTimeout), wait.WithImmediate())
	if err != nil {
		return fmt.Errorf("waiting for StatefulSet %s/%s: expected current generation observed and all desired replicas ready; last observed: %s: %w",
			opts.Namespace, opts.Name, last, err)
	}
	return nil
}

// Workload describes one Deployment, DaemonSet, or StatefulSet.
type Workload struct {
	Kind string
	Name string

	// Generation changes when the workload spec changes.
	Generation int64

	// HasOwner reports whether the workload has owner references.
	HasOwner bool
}

func (w Workload) String() string { return w.Kind + "/" + w.Name }

// ListWorkloads describes every Deployment, DaemonSet, and StatefulSet in a
// namespace, ordered by kind and name.
func ListWorkloads(ctx context.Context, client klient.Client, namespace string) ([]Workload, error) {
	var workloads []Workload

	var deployments appsv1.DeploymentList
	err := client.Resources(namespace).List(ctx, &deployments)
	if err != nil {
		return nil, fmt.Errorf("listing Deployments in %s: %w", namespace, err)
	}
	for _, d := range deployments.Items {
		workloads = append(workloads, Workload{
			Kind: "Deployment", Name: d.Name, Generation: d.Generation,
			HasOwner: len(d.OwnerReferences) > 0,
		})
	}

	var daemonsets appsv1.DaemonSetList
	err = client.Resources(namespace).List(ctx, &daemonsets)
	if err != nil {
		return nil, fmt.Errorf("listing DaemonSets in %s: %w", namespace, err)
	}
	for _, ds := range daemonsets.Items {
		workloads = append(workloads, Workload{
			Kind: "DaemonSet", Name: ds.Name, Generation: ds.Generation,
			HasOwner: len(ds.OwnerReferences) > 0,
		})
	}

	var statefulsets appsv1.StatefulSetList
	err = client.Resources(namespace).List(ctx, &statefulsets)
	if err != nil {
		return nil, fmt.Errorf("listing StatefulSets in %s: %w", namespace, err)
	}
	for _, ss := range statefulsets.Items {
		workloads = append(workloads, Workload{
			Kind: "StatefulSet", Name: ss.Name, Generation: ss.Generation,
			HasOwner: len(ss.OwnerReferences) > 0,
		})
	}

	sort.Slice(workloads, func(i, j int) bool { return workloads[i].String() < workloads[j].String() })
	return workloads, nil
}

// CreateDaemonSet creates a DaemonSet and registers deletion with t.Cleanup.
func CreateDaemonSet(ctx context.Context, t *testing.T, client klient.Client, daemonSet *appsv1.DaemonSet) *appsv1.DaemonSet {
	t.Helper()
	created := daemonSet.DeepCopy()
	require.NoError(t, client.Resources(created.Namespace).Create(ctx, created),
		"creating DaemonSet %s/%s", created.Namespace, created.Name)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		require.NoError(t, DeleteDaemonSet(cleanupCtx, client, created),
			"deleting DaemonSet %s/%s and its pods", created.Namespace, created.Name)
	})
	return created
}

// DeleteDaemonSet deletes a DaemonSet and waits for it and its selected pods
// to disappear. An absent DaemonSet is allowed; the context bounds the wait.
func DeleteDaemonSet(ctx context.Context, client klient.Client, daemonSet *appsv1.DaemonSet) error {
	selector, err := metav1.LabelSelectorAsSelector(daemonSet.Spec.Selector)
	if err != nil {
		return fmt.Errorf("reading DaemonSet %s/%s selector: %w", daemonSet.Namespace, daemonSet.Name, err)
	}
	r := client.Resources(daemonSet.Namespace)
	if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting DaemonSet %s/%s: %w", daemonSet.Namespace, daemonSet.Name, err)
	}
	return wait.For(func(ctx context.Context) (bool, error) {
		var current appsv1.DaemonSet
		err := r.Get(ctx, daemonSet.Name, daemonSet.Namespace, &current)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		var pods corev1.PodList
		if listErr := r.List(ctx, &pods, resources.WithLabelSelector(selector.String())); listErr != nil {
			return false, listErr
		}
		return apierrors.IsNotFound(err) && len(pods.Items) == 0, nil
	}, wait.WithContext(ctx), wait.WithTimeout(0), wait.WithInterval(2*time.Second), wait.WithImmediate())
}

// CreateFixturePod creates a pod with cleanup and failure diagnostics.
func CreateFixturePod(ctx context.Context, t *testing.T, client klient.Client, ns string, pod *corev1.Pod) (*corev1.Pod, func()) {
	t.Helper()
	created := pod.DeepCopy()
	created.Namespace = ns
	r := client.Resources(ns)
	require.NoError(t, r.Create(ctx, created), "creating fixture pod in %s", ns)
	cleanup := sync.OnceFunc(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute+30*time.Second)
		defer cancel()
		if t.Failed() {
			logFixturePodDiagnostics(cleanupCtx, t, client, created)
		}
		err := r.Delete(cleanupCtx, created, func(opts *metav1.DeleteOptions) {
			opts.Preconditions = &metav1.Preconditions{UID: &created.UID}
		})
		if !apierrors.IsNotFound(err) {
			assert.NoError(t, err, "deleting fixture pod %s/%s", ns, created.Name)
		}
		assert.NoError(t, wait.For(conditions.New(r).ResourcesDeleted(&corev1.PodList{Items: []corev1.Pod{*created}}),
			wait.WithContext(cleanupCtx), wait.WithTimeout(0), wait.WithInterval(time.Second), wait.WithImmediate()),
			"waiting for fixture pod %s/%s removal", ns, created.Name)
	})
	t.Cleanup(cleanup)
	return created, cleanup
}

// logFixturePodDiagnostics logs status, recent logs, and events for a failed fixture.
func logFixturePodDiagnostics(ctx context.Context, t *testing.T, client klient.Client, pod *corev1.Pod) {
	t.Helper()
	ns := pod.Namespace
	r := client.Resources(ns)
	var current corev1.Pod
	err := r.Get(ctx, pod.Name, ns, &current)
	if err == nil {
		t.Logf("fixture %s/%s: %+v", ns, pod.Name, current.Status)
	}
	proxyClient, err := coreclient.NewForConfig(client.RESTConfig())
	if err != nil {
		t.Logf("fixture %s/%s log client: %v", ns, pod.Name, err)
	} else {
		logs, err := proxyClient.Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{LimitBytes: new(int64(16 << 10))}).DoRaw(ctx)
		t.Logf("fixture %s/%s logs: error=%v\n%s", ns, pod.Name, err, logs)
	}
	var events corev1.EventList
	err = r.List(ctx, &events, resources.WithFieldSelector("involvedObject.uid="+string(pod.UID)))
	if err == nil {
		for _, event := range events.Items {
			t.Logf("fixture %s/%s event: %s: %s", ns, pod.Name, event.Reason, event.Message)
		}
	}
}
