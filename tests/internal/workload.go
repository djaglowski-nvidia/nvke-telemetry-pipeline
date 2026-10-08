package workload

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/wait"
)

const workloadWaitTimeout = 15 * time.Minute

type DeploymentWaitOptions struct {
	Name, Namespace string
	Client          klient.Client
	Rollout         bool
	ReplicaCount    *int32
}

// WaitForDeployment waits for availability, or full rollout completion when
// Rollout is set. ReplicaCount optionally requires spec.replicas to match that
// count. The wait is capped at 15 minutes or the caller's earlier deadline.
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

// WaitForDaemonSet waits for the current generation to be observed and all
// scheduled replicas to be updated, ready, and available. A non-nil NodeCount
// also requires that many scheduled replicas. The wait is capped at 15 minutes
// or the caller's earlier deadline.
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

// WaitForStatefulSet waits for the current generation to be observed and the
// desired replicas to be ready. It does not verify upgrade rollout completion.
// The wait is capped at 15 minutes or the caller's earlier deadline.
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
