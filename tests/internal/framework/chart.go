// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package framework coordinates reusable NKTP test setup and cleanup.
package framework

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA-dev/nktp/tests/internal/helm"
	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

// Collectors identifies the OpenTelemetryCollector resources NKTP deploys.
var Collectors = schema.GroupVersionResource{
	Group: "opentelemetry.io", Version: "v1beta1", Resource: "opentelemetrycollectors",
}

// Chart owns the inputs needed to install and settle an NKTP release.
type Chart struct {
	Helm       *helm.Client
	Kube       *kube.Client
	Release    helm.Release
	Path       string
	ValueFiles []string
	Timeout    time.Duration
}

// InstallFresh refuses to adopt an existing release, installs the chart, and
// waits for the workloads created later by the OpenTelemetry Operator. Its
// boolean result reports ownership once Helm succeeds, including when waiting
// for the Operator-derived workloads later fails.
func (c *Chart) InstallFresh(ctx context.Context) (bool, error) {
	exists, err := c.Helm.Installed(ctx, c.Release)
	if err != nil {
		return false, err
	}
	if exists {
		return false, fmt.Errorf("release %s already exists in namespace %s", c.Release.Name, c.Release.Namespace)
	}
	return c.Apply(ctx)
}

// Apply installs or reapplies the chart and waits for all NKTP workloads. It
// reports whether Helm successfully applied the release even when the later
// readiness check fails, so a caller that owns a fresh release can remove it.
func (c *Chart) Apply(ctx context.Context) (bool, error) {
	if err := c.Helm.Install(ctx, c.Release, c.Path, c.ValueFiles, c.Timeout); err != nil {
		return false, err
	}
	return true, c.WaitReady(ctx)
}

// WaitReady waits for the workloads the Operator derives from Collector CRs,
// in addition to the resources Helm already waited for.
func (c *Chart) WaitReady(ctx context.Context) error {
	collectors, err := c.Kube.CustomResources(ctx, c.Release.Namespace, Collectors)
	if err != nil {
		return err
	}
	expected, err := CollectorWorkloads(collectors)
	if err != nil {
		return err
	}
	if err := c.Kube.WaitWorkloadsExist(ctx, c.Release.Namespace, expected); err != nil {
		return err
	}
	return c.Kube.WaitWorkloadsReady(ctx, c.Release.Namespace)
}

// Uninstall removes the Helm release. Operator-derived workloads may remain
// briefly and can be awaited with kube.Client.WaitWorkloadsGone.
func (c *Chart) Uninstall(ctx context.Context) error {
	return c.Helm.Uninstall(ctx, c.Release, c.Timeout)
}

// CollectorWorkloads names the workloads the Operator builds from Collector
// CRs: one collector workload, plus a Target Allocator when enabled.
func CollectorWorkloads(crs []unstructured.Unstructured) ([]string, error) {
	var expected []string
	for _, cr := range crs {
		expected = append(expected, cr.GetName()+"-collector")
		allocator, _, err := unstructured.NestedBool(cr.Object, "spec", "targetAllocator", "enabled")
		if err != nil {
			return nil, fmt.Errorf("reading the target allocator of %s: %w", cr.GetName(), err)
		}
		if allocator {
			expected = append(expected, cr.GetName()+"-targetallocator")
		}
	}
	return expected, nil
}
