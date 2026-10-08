// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	workload "github.com/NVIDIA-dev/nktp/tests/internal"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/kube"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var (
	testenv    env.Environment
	chartPath  = flag.String("chart", "../../nktp", "Path to the Helm chart under test")
	valuesPath = flag.String("values", "values.yaml", "Path to the Helm values file")
)

const releaseName = "nktp-e2e"

func TestMain(t *testing.M) {
	var helmcfg *action.Configuration
	cfg, err := envconf.NewFromFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error while creating env config: %v\n", err)
		os.Exit(1)
	}

	if cfg.Namespace() == "" {
		cfg.WithNamespace("nktp")
	}

	ensureCleanup := func(ctx context.Context, cfg *envconf.Config) error {
		releaseNamespace := cfg.Namespace()
		uninstall := action.NewUninstall(helmcfg)
		uninstall.Timeout = time.Minute * 5
		uninstall.WaitStrategy = kube.StatusWatcherStrategy
		uninstall.IgnoreNotFound = true
		if _, err := uninstall.Run(releaseName); err != nil {
			return fmt.Errorf("uninstalling chart: %w", err)
		}
		client, err := cfg.NewClient()
		if err != nil {
			return fmt.Errorf("creating client for lease cleanup: %w", err)
		}
		resources := client.Resources(releaseNamespace)
		var leases coordinationv1.LeaseList
		if err := resources.List(ctx, &leases); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("listing leases in %s: %w", releaseNamespace, err)
		}
		for _, lease := range leases.Items {
			if !strings.HasSuffix(lease.Name, ".opentelemetry.io") {
				continue
			}
			if err := resources.Delete(ctx, &lease); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting lease %s/%s: %w", releaseNamespace, lease.Name, err)
			}
		}
		return nil
	}

	testenv = env.NewWithConfig(cfg)
	testenv.Setup(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		releaseNamespace := cfg.Namespace()
		slog.Info("cleaning up any pre-existing deployed nktp charts")
		getter := &genericclioptions.ConfigFlags{
			KubeConfig: new(cfg.KubeconfigFile()),
			Namespace:  new(cfg.Namespace()),
		}
		helmcfg = action.NewConfiguration()
		err := helmcfg.Init(getter, cfg.Namespace(), "secret")
		if err != nil {
			return ctx, fmt.Errorf("failed to create helm install config: %w", err)
		}
		if err := ensureCleanup(ctx, cfg); err != nil {
			return ctx, fmt.Errorf("cleaning up helm chart in target cluster: %w", err)
		}
		chart, err := loader.Load(*chartPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to load chart at target path %s: %w", *chartPath, err)
		}
		values, err := common.ReadValuesFile(*valuesPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to load values file at target path %s: %w", *valuesPath, err)
		}
		install := action.NewInstall(helmcfg)
		install.ReleaseName = releaseName
		install.Namespace = releaseNamespace
		install.CreateNamespace = true
		install.WaitStrategy = kube.StatusWatcherStrategy
		install.Timeout = 5 * time.Minute
		install.WaitForJobs = true
		slog.Info("installing chart to target cluster", "release", releaseName, "namespace", releaseNamespace)
		if _, err = install.RunWithContext(ctx, chart, values); err != nil {
			return ctx, fmt.Errorf("failed to helm install: %w", err)
		}
		slog.Info("setup complete, starting suite")
		return ctx, nil
	}).Finish(func(ctx context.Context, c *envconf.Config) (context.Context, error) {
		slog.Info("uninstalling chart from target cluster")
		if err := ensureCleanup(ctx, c); err != nil {
			return ctx, fmt.Errorf("uninstalling chart: %w", err)
		}
		return ctx, nil
	})

	os.Exit(testenv.Run(t))
}

func TestOperatorCollectorsReady(t *testing.T) {
	var collectors unstructured.UnstructuredList
	f := features.New("OpenTelemetry collector operator").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			releaseNamespace := c.Namespace()
			collectors.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "opentelemetry.io",
				Version: "v1beta1",
				Kind:    "OpenTelemetryCollectorList",
			})
			require.NoError(t, c.Client().Resources(releaseNamespace).List(ctx, &collectors), "listing collector CRs")
			require.NotEmpty(t, collectors.Items, "no OpenTelemetryCollector resources found in %s", releaseNamespace)
			return ctx
		}).
		Assess("Deployments are ready", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			releaseNamespace := c.Namespace()
			clone := slices.Clone(collectors.Items)
			expected := slices.DeleteFunc(clone, func(c unstructured.Unstructured) bool {
				mode, _, _ := unstructured.NestedString(c.Object, "spec", "mode")
				return mode != "deployment"
			})
			waitCtx, cancel := context.WithTimeout(ctx, time.Minute*5)
			defer cancel()
			client := c.Client()
			for _, collector := range expected {
				name := collector.GetName() + "-collector"
				require.NoError(t, workload.WaitForDeployment(waitCtx, workload.DeploymentWaitOptions{
					Name: name, Namespace: releaseNamespace, Client: client,
				}), "waiting for Deployment %s/%s", releaseNamespace, name)
			}
			return ctx
		}).
		Assess("Daemonsets are ready", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			releaseNamespace := c.Namespace()
			expected := slices.DeleteFunc(slices.Clone(collectors.Items), func(c unstructured.Unstructured) bool {
				mode, _, _ := unstructured.NestedString(c.Object, "spec", "mode")
				return mode != "daemonset"
			})
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			client := c.Client()
			for _, collector := range expected {
				name := collector.GetName() + "-collector"
				require.NoError(t, workload.WaitForDaemonSet(waitCtx, workload.DaemonSetWaitOptions{
					Name: name, Namespace: releaseNamespace, Client: client,
				}), "waiting for DaemonSet %s/%s", releaseNamespace, name)
			}
			return ctx
		}).
		Assess("Statefulsets are ready", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			releaseNamespace := c.Namespace()
			expected := slices.DeleteFunc(slices.Clone(collectors.Items), func(c unstructured.Unstructured) bool {
				mode, _, _ := unstructured.NestedString(c.Object, "spec", "mode")
				return mode != "statefulset"
			})
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			client := c.Client()
			for _, collector := range expected {
				name := collector.GetName() + "-collector"
				require.NoError(t, workload.WaitForStatefulSet(waitCtx, workload.StatefulSetWaitOptions{
					Name: name, Namespace: releaseNamespace, Client: client,
				}), "waiting for StatefulSet %s/%s", releaseNamespace, name)
			}
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}
