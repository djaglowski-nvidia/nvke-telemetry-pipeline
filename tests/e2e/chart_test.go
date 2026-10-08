// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA-dev/nktp/tests/internal/framework"
)

// The node exporter labels every object it owns with its chart name, which is
// stable across release names.
const nodeExporterSelector = "app.kubernetes.io/name=node-exporter"

var serviceMonitors = schema.GroupVersionResource{
	Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors",
}

// Shared validation returns descriptive errors and never interacts with
// testing.T. Each Test lists its coverage explicitly and reports failures.
func checkNodeExporterCoverage(ctx context.Context) error {
	daemonsets, err := k.DaemonSets(ctx, *namespace, nodeExporterSelector)
	if err != nil {
		return fmt.Errorf("listing node exporter DaemonSets: %w", err)
	}
	if len(daemonsets) != 1 {
		return fmt.Errorf("got %d node exporter DaemonSets, want 1", len(daemonsets))
	}
	nodes, err := k.NodeCount(ctx)
	if err != nil {
		return fmt.Errorf("counting nodes: %w", err)
	}
	status := daemonsets[0].Status
	var errs []error
	if status.DesiredNumberScheduled != int32(nodes) {
		errs = append(errs, fmt.Errorf("node exporter desired pods = %d, want %d (one per node)", status.DesiredNumberScheduled, nodes))
	}
	if status.NumberReady != status.DesiredNumberScheduled {
		errs = append(errs, fmt.Errorf("node exporter ready pods = %d, want %d", status.NumberReady, status.DesiredNumberScheduled))
	}
	return errors.Join(errs...)
}

func checkNodeExporterMetrics(ctx context.Context) error {
	pods, err := k.Pods(ctx, *namespace, nodeExporterSelector)
	if err != nil {
		return fmt.Errorf("listing node exporter pods: %w", err)
	}
	if len(pods) == 0 {
		return errors.New("no node exporter pods to scrape")
	}
	body, err := k.PodEndpoint(ctx, *namespace, pods[0].Name, "9100", "/metrics")
	if err != nil {
		return fmt.Errorf("scraping the node exporter: %w", err)
	}
	if !strings.Contains(string(body), "node_cpu_seconds_total") {
		return errors.New("node exporter metrics do not contain node_cpu_seconds_total")
	}
	return nil
}

func checkNodeExporterMonitor(ctx context.Context) error {
	monitors, err := k.CustomResources(ctx, *namespace, serviceMonitors)
	if err != nil {
		return fmt.Errorf("listing ServiceMonitors: %w", err)
	}
	got, want := names(monitors), *release+"-node-exporter"
	if !slices.Contains(got, want) {
		return fmt.Errorf("ServiceMonitors = %v, want to include %s", got, want)
	}
	return nil
}

func checkGrafanaHealth(ctx context.Context) error {
	body, err := k.ServiceEndpoint(ctx, *namespace, "lgtm", "3000", "/api/health")
	if err != nil {
		return fmt.Errorf("reaching Grafana through the lgtm service: %w", err)
	}
	if !strings.Contains(string(body), "database") {
		return errors.New("Grafana health response does not contain database status")
	}
	return nil
}

func checkCollectorDeployment(ctx context.Context) error {
	collectors, err := k.CustomResources(ctx, *namespace, framework.Collectors)
	if err != nil {
		return fmt.Errorf("listing Collectors: %w", err)
	}
	// Adding a collector to the chart means adding it here.
	var want []string
	for _, collector := range []string{
		"cluster-events", "daemonset", "gateway-logs", "gateway-metrics",
		"gateway-traces", "large-target-metrics", "watchdog",
	} {
		want = append(want, *release+"-"+collector)
	}
	got := names(collectors)
	slices.Sort(got)
	slices.Sort(want)
	if diff := cmp.Diff(want, got); diff != "" {
		return fmt.Errorf("Collectors mismatch (-want +got):\n%s", diff)
	}
	return nil
}

func checkCollectorReadiness(ctx context.Context) error {
	collectors, err := k.CustomResources(ctx, *namespace, framework.Collectors)
	if err != nil {
		return fmt.Errorf("listing Collectors: %w", err)
	}
	expected, err := framework.CollectorWorkloads(collectors)
	if err != nil {
		return fmt.Errorf("deriving Collector workloads: %w", err)
	}
	workloads, err := k.Workloads(ctx, *namespace)
	if err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}
	ready := make(map[string]bool, len(workloads))
	for _, workload := range workloads {
		ready[workload.Name] = workload.Ready
	}
	var errs []error
	for _, name := range expected {
		if isReady, exists := ready[name]; !exists {
			errs = append(errs, fmt.Errorf("expected workload %s is missing", name))
		} else if !isReady {
			errs = append(errs, fmt.Errorf("workload %s is not ready", name))
		}
	}
	return errors.Join(errs...)
}

func checkRequiredCRDs(ctx context.Context) error {
	var errs []error
	for _, crd := range []struct{ group, resource string }{
		{"monitoring.coreos.com", "servicemonitors"},
		{"monitoring.coreos.com", "podmonitors"},
		{"monitoring.coreos.com", "probes"},
		{"monitoring.coreos.com", "scrapeconfigs"},
		{"opentelemetry.io", "opentelemetrycollectors"},
	} {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		served, err := k.ServesResource(crd.group, crd.resource)
		if err != nil {
			errs = append(errs, fmt.Errorf("discovering %s.%s: %w", crd.resource, crd.group, err))
		} else if !served {
			errs = append(errs, fmt.Errorf("%s.%s must be served", crd.resource, crd.group))
		}
	}
	return errors.Join(errs...)
}
