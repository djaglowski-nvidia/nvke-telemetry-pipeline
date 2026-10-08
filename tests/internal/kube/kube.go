// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package kube holds the Kubernetes API helpers shared by the test suites.
//
// The helpers here report what a cluster is doing and return what they find.
// Deciding whether that amounts to a pass or a failure belongs to the suites,
// so nothing in this package depends on a test.
package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// pollInterval is how often the Wait helpers ask the API server again.
const pollInterval = 2 * time.Second

// Client talks to one test cluster.
type Client struct {
	kubernetes.Interface

	// The typed client above covers the built-in API groups only. Reaching a
	// custom resource without generated types for it needs this one.
	dynamic dynamic.Interface
}

// New returns a client for the cluster one kubeconfig file describes. The path
// is explicit rather than discovered, so a caller cannot reach a cluster
// through an inherited KUBECONFIG.
func New(kubeconfig string) (*Client, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %s: %w", kubeconfig, err)
	}
	// Disable client-side throttling for test clients. Polling deadlines should
	// reflect API responses rather than time spent waiting for rate-limit tokens.
	config.QPS = -1
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("building a client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("building a dynamic client: %w", err)
	}
	return &Client{Interface: clientset, dynamic: dynamicClient}, nil
}

// Workload describes one Deployment, DaemonSet, or StatefulSet.
type Workload struct {
	Kind string
	Name string

	// Generation moves when a spec does, which a name does not: a template
	// that renders differently on each pass rolls every pod while leaving
	// every name identical.
	Generation int64

	// Derived reports whether another object owns this workload. A controller
	// owns what it builds from a custom resource, while nothing owns what Helm
	// applies itself.
	Derived bool

	// Ready holds the same conclusion `kubectl rollout status` reaches: the
	// controller has caught up with the current spec, and every replica it
	// wants is up.
	Ready bool
}

func (w Workload) String() string { return w.Kind + "/" + w.Name }

// Workloads describes every Deployment, DaemonSet, and StatefulSet in a
// namespace, ordered by kind and name.
func (c *Client) Workloads(ctx context.Context, namespace string) ([]Workload, error) {
	var workloads []Workload

	deployments, err := c.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing Deployments in %s: %w", namespace, err)
	}
	for _, d := range deployments.Items {
		want := replicas(d.Spec.Replicas)
		workloads = append(workloads, Workload{
			Kind: "Deployment", Name: d.Name, Generation: d.Generation,
			Derived: len(d.OwnerReferences) > 0,
			Ready: d.Status.ObservedGeneration >= d.Generation &&
				d.Status.UpdatedReplicas == want &&
				d.Status.ReadyReplicas == want &&
				d.Status.AvailableReplicas == want,
		})
	}

	daemonsets, err := c.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing DaemonSets in %s: %w", namespace, err)
	}
	for _, ds := range daemonsets.Items {
		want := ds.Status.DesiredNumberScheduled
		workloads = append(workloads, Workload{
			Kind: "DaemonSet", Name: ds.Name, Generation: ds.Generation,
			Derived: len(ds.OwnerReferences) > 0,
			Ready: ds.Status.ObservedGeneration >= ds.Generation &&
				ds.Status.UpdatedNumberScheduled == want &&
				ds.Status.NumberReady == want &&
				ds.Status.NumberAvailable == want,
		})
	}

	statefulsets, err := c.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing StatefulSets in %s: %w", namespace, err)
	}
	for _, ss := range statefulsets.Items {
		want := replicas(ss.Spec.Replicas)
		workloads = append(workloads, Workload{
			Kind: "StatefulSet", Name: ss.Name, Generation: ss.Generation,
			Derived: len(ss.OwnerReferences) > 0,
			Ready: ss.Status.ObservedGeneration >= ss.Generation &&
				ss.Status.UpdatedReplicas == want &&
				ss.Status.ReadyReplicas == want &&
				ss.Status.CurrentRevision == ss.Status.UpdateRevision,
		})
	}

	sort.Slice(workloads, func(i, j int) bool { return workloads[i].String() < workloads[j].String() })
	return workloads, nil
}

// replicas reports how many replicas a spec asks for, applying the default the
// API server would.
func replicas(spec *int32) int32 {
	if spec == nil {
		return 1
	}
	return *spec
}

// WaitWorkloadsExist waits for a workload of any kind to exist under each of
// the given names. It is how a caller waits for what a controller builds after
// the objects it watches are applied.
func (c *Client) WaitWorkloadsExist(ctx context.Context, namespace string, names []string) error {
	var missing []string
	err := wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		workloads, err := c.Workloads(ctx, namespace)
		if err != nil {
			return false, err
		}
		found := make(map[string]bool, len(workloads))
		for _, w := range workloads {
			found[w.Name] = true
		}
		missing = nil
		for _, name := range names {
			if !found[name] {
				missing = append(missing, name)
			}
		}
		return len(missing) == 0, nil
	})
	if wait.Interrupted(err) && len(missing) > 0 {
		return fmt.Errorf("%w: no workload exists named %s", err, strings.Join(missing, ", "))
	}
	return err
}

// WaitWorkloadsReady waits for every workload in a namespace to finish rolling
// out. Workloads that appear while it is waiting are waited for too.
func (c *Client) WaitWorkloadsReady(ctx context.Context, namespace string) error {
	var unready []string
	err := wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		workloads, err := c.Workloads(ctx, namespace)
		if err != nil {
			return false, err
		}
		unready = nil
		for _, w := range workloads {
			if !w.Ready {
				unready = append(unready, w.String())
			}
		}
		return len(unready) == 0, nil
	})
	if wait.Interrupted(err) && len(unready) > 0 {
		return fmt.Errorf("%w: still waiting on %s", err, strings.Join(unready, ", "))
	}
	return err
}

// WaitWorkloadsGone waits for a namespace to hold no workloads at all.
func (c *Client) WaitWorkloadsGone(ctx context.Context, namespace string) error {
	var left []string
	err := wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		workloads, err := c.Workloads(ctx, namespace)
		if err != nil {
			return false, err
		}
		left = nil
		for _, w := range workloads {
			left = append(left, w.String())
		}
		return len(left) == 0, nil
	})
	if wait.Interrupted(err) && len(left) > 0 {
		return fmt.Errorf("%w: %s is still there", err, strings.Join(left, ", "))
	}
	return err
}

func (c *Client) ListLeases(ctx context.Context, namespace string) (*coordinationv1.LeaseList, error) {
	ll, err := c.CoordinationV1().Leases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &coordinationv1.LeaseList{}, nil
		}
		return ll, fmt.Errorf("listing leases failed in namespace %s: %w", namespace, err)
	}
	return ll, nil
}

func (c *Client) DeleteLease(ctx context.Context, namespace, name string) error {
	err := c.CoordinationV1().Leases(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting lease %s/%s: %w", namespace, name, err)
	}
	return nil
}

// CustomResources returns every object of one custom resource kind in a
// namespace, ordered by name.
func (c *Client) CustomResources(ctx context.Context, namespace string, resource schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	list, err := c.dynamic.Resource(resource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing %s in %s: %w", resource.Resource, namespace, err)
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	return items, nil
}

// CreateCustomResource submits one unstructured object to the API server.
// Admission errors are returned unchanged so callers can distinguish a
// webhook denial from schema validation or an unreachable webhook.
func (c *Client) CreateCustomResource(ctx context.Context, namespace string, resource schema.GroupVersionResource, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return c.dynamic.Resource(resource).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
}

// DeleteeCustomResource deletes one unstructured object from the API server.
func (c *Client) DeleteCustomResource(ctx context.Context, namespace string, resource schema.GroupVersionResource, name string) error {
	return c.dynamic.Resource(resource).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// DaemonSets returns the DaemonSets matching a label selector.
func (c *Client) DaemonSets(ctx context.Context, namespace, selector string) ([]appsv1.DaemonSet, error) {
	list, err := c.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing DaemonSets matching %q: %w", selector, err)
	}
	return list.Items, nil
}

// Pods returns the pods matching a label selector.
func (c *Client) Pods(ctx context.Context, namespace, selector string) ([]corev1.Pod, error) {
	list, err := c.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing pods matching %q: %w", selector, err)
	}
	return list.Items, nil
}

// NodeCount returns the number of nodes in the cluster.
func (c *Client) NodeCount(ctx context.Context) (int, error) {
	list, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("listing nodes: %w", err)
	}
	return len(list.Items), nil
}

// PodEndpoint fetches a path from a pod's port through the API server's proxy.
//
// The proxy needs nothing installed in the target container, unlike exec
// against a distroless image, and no local port plumbing, unlike
// port-forwarding.
func (c *Client) PodEndpoint(ctx context.Context, namespace, pod, port, path string) ([]byte, error) {
	return c.CoreV1().Pods(namespace).ProxyGet("http", pod, port, path, nil).DoRaw(ctx)
}

// ServiceEndpoint fetches a path from a service's port through the API
// server's proxy, which also exercises the service's endpoint selection.
func (c *Client) ServiceEndpoint(ctx context.Context, namespace, service, port, path string) ([]byte, error) {
	return c.CoreV1().Services(namespace).ProxyGet("http", service, port, path, nil).DoRaw(ctx)
}

// ServiceEndpointQuery fetches a path with query parameters from a service's
// port through the API server's proxy.
func (c *Client) ServiceEndpointQuery(ctx context.Context, namespace, service, port, path string, query map[string]string) ([]byte, error) {
	return c.CoreV1().Services(namespace).ProxyGet("http", service, port, path, query).DoRaw(ctx)
}

// ServesResource reports whether the API server serves a resource in an API
// group, in any version. For a custom resource that means its definition is
// installed and established.
//
// It takes no context because client-go's discovery client accepts none.
func (c *Client) ServesResource(group, resource string) (bool, error) {
	groups, err := c.Discovery().ServerGroups()
	if err != nil {
		return false, fmt.Errorf("discovering API groups: %w", err)
	}
	for _, g := range groups.Groups {
		if g.Name != group {
			continue
		}
		for _, version := range g.Versions {
			resources, err := c.Discovery().ServerResourcesForGroupVersion(version.GroupVersion)
			if err != nil {
				return false, fmt.Errorf("discovering resources in %s: %w", version.GroupVersion, err)
			}
			for _, r := range resources.APIResources {
				if r.Name == resource {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
