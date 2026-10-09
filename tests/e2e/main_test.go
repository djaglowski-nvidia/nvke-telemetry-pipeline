// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/internal/config"
	"github.com/NVIDIA-dev/nktp/tests/internal/funcs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

var testenv = config.NewEnvironmentFromFlags()

const releaseName = "nktp-e2e"

func TestMain(t *testing.M) {
	cfg, err := envconf.NewFromFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error while creating env config: %v\n", err)
		os.Exit(1)
	}
	testenv.SetEnvironment(env.NewWithConfig(cfg))
	testenv.Install()
	testenv.Setup(waitForCollectors)
	testenv.Uninstall()
	os.Exit(testenv.Run(t))
}

func waitForCollectors(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	namespace := cfg.Namespace()
	client := cfg.Client()
	collectors := &unstructured.UnstructuredList{}
	collectors.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "opentelemetry.io",
		Version: "v1beta1",
		Kind:    "OpenTelemetryCollectorList",
	})
	if err := client.Resources(namespace).List(ctx, collectors); err != nil {
		return ctx, fmt.Errorf("listing Collector resources: %w", err)
	}
	if len(collectors.Items) == 0 {
		return ctx, fmt.Errorf("no Collector resources found in namespace %s", namespace)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	targetAllocators := 0
	for _, collector := range collectors.Items {
		mode, found, err := unstructured.NestedString(collector.Object, "spec", "mode")
		if err != nil {
			return ctx, fmt.Errorf("reading mode for Collector %s: %w", collector.GetName(), err)
		}
		if !found {
			return ctx, fmt.Errorf("Collector %s does not specify a mode", collector.GetName())
		}

		name := collector.GetName() + "-collector"
		switch mode {
		case "deployment":
			err = funcs.WaitForDeployment(waitCtx, funcs.DeploymentWaitOptions{Name: name, Namespace: namespace, Client: client})
		case "daemonset":
			err = funcs.WaitForDaemonSet(waitCtx, funcs.DaemonSetWaitOptions{Name: name, Namespace: namespace, Client: client})
		case "statefulset":
			err = funcs.WaitForStatefulSet(waitCtx, funcs.StatefulSetWaitOptions{Name: name, Namespace: namespace, Client: client})
		default:
			return ctx, fmt.Errorf("Collector %s has unsupported mode %q", collector.GetName(), mode)
		}
		if err != nil {
			return ctx, fmt.Errorf("waiting for %s Collector workload %s/%s: %w", mode, namespace, name, err)
		}

		enabled, found, err := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "enabled")
		if err != nil {
			return ctx, fmt.Errorf("reading Target Allocator configuration for Collector %s: %w", collector.GetName(), err)
		}
		if !found || !enabled {
			continue
		}
		targetAllocators++
		name = collector.GetName() + "-targetallocator"
		if err := funcs.WaitForDeployment(waitCtx, funcs.DeploymentWaitOptions{Name: name, Namespace: namespace, Client: client}); err != nil {
			return ctx, fmt.Errorf("waiting for Target Allocator Deployment %s/%s: %w", namespace, name, err)
		}
	}
	if targetAllocators == 0 {
		return ctx, fmt.Errorf("no Collectors have a Target Allocator enabled")
	}
	return ctx, nil
}
