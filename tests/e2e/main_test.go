//go:build e2e

// Package e2e asserts user-visible NKTP behavior on a Kubernetes cluster.
// With -nktp.install=true each test installs the chart before its assertions;
// with -nktp.install=false each test requires the configured release to exist.
package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA-dev/nktp/tests/internal/framework"
	"github.com/NVIDIA-dev/nktp/tests/internal/helm"
	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

var (
	kubeconfig = flag.String("nktp.kubeconfig", os.Getenv("KUBECONFIG"),
		"kubeconfig of the cluster to test against")
	chart = flag.String("nktp.chart", "../../nktp",
		"path to the chart under test, whose dependencies must already be built")
	release = flag.String("nktp.release", "nktp",
		"name to install the release under")
	namespace = flag.String("nktp.namespace", "nktp",
		"namespace to install the release into")
	timeout = flag.Duration("nktp.timeout", 15*time.Minute,
		"how long any one install, rollout, or removal may take")
	install = flag.Bool("nktp.install", true,
		"true installs and removes a fresh release for each test; false requires and reuses the named existing release without modifying it")
	stableReapply = flag.Bool("nktp.stable-reapply", true,
		"fail when reapplying the release moves a workload Helm manages; unset once the chart generates anything at install time, which moves them for a reason of its own")

	valueFiles valuesFlag
)

// valuesFlag collects a repeatable -nktp.values in the order given, which is
// the order Helm layers the files in.
type valuesFlag []string

func (v *valuesFlag) String() string { return strings.Join(*v, " ") }

func (v *valuesFlag) Set(path string) error {
	*v = append(*v, path)
	return nil
}

func init() {
	flag.Var(&valueFiles, "nktp.values", "values file to layer on the chart defaults (repeatable)")
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *kubeconfig == "" {
		fmt.Fprintln(os.Stderr,
			"No cluster to test against: set KUBECONFIG, or pass -nktp.kubeconfig. Run `task e2e:test` to have a throwaway one created.")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// newKube returns a client for the Kubernetes API of the cluster under test.
func newKube(t *testing.T) *kube.Client {
	t.Helper()

	k, err := kube.New(*kubeconfig)
	require.NoError(t, err, "connecting to the cluster")
	return k
}

// newHelm returns a Helm client that logs to the test given to it, so build
// one per test rather than sharing one: `go test` files a log line under
// whichever test wrote it, not under the one running at the time.
func newHelm(t *testing.T) *helm.Client {
	t.Helper()
	return &helm.Client{Kubeconfig: *kubeconfig, Log: logWriter{t}}
}

// newChart returns an installer for the configured release.
func newChart(t *testing.T, k *kube.Client) *framework.Chart {
	t.Helper()
	return &framework.Chart{
		Helm:       newHelm(t),
		Kube:       k,
		Release:    releaseRef(),
		Path:       *chart,
		ValueFiles: valueFiles,
		Timeout:    *timeout,
	}
}

// readyRelease makes the configured release ready by installing it or waiting
// for an existing one, according to -nktp.install. It uses the supplied client
// and returns the chart for tests that need to modify the release.
func readyRelease(ctx context.Context, t *testing.T, k *kube.Client) *framework.Chart {
	t.Helper()
	deployed := newChart(t, k)

	ctx, cancel := deadline(ctx)
	defer cancel()

	if *install {
		t.Logf("installing a fresh release %v/%v (all OpenTelemetryCollectors must be Ready)", deployed.Release.Namespace, deployed.Release.Name)
		owned, err := deployed.InstallFresh(ctx)
		if owned {
			registerChartCleanup(t, deployed, k)
		}
		require.NoError(t, err, "installing a fresh release %v/%v", deployed.Release.Namespace, deployed.Release.Name)
	} else {
		t.Logf("waiting for the shared release %v/%v (all OpenTelemetryCollectors must be Ready)", deployed.Release.Namespace, deployed.Release.Name)
		exists, err := deployed.Helm.Installed(ctx, deployed.Release)
		require.NoError(t, err, "checking for the shared release %v/%v", deployed.Release.Namespace, deployed.Release.Name)
		require.True(t, exists,
			"release %s must already exist in namespace %s when -nktp.install=false",
			deployed.Release.Name, deployed.Release.Namespace)
		require.NoError(t, deployed.WaitReady(ctx), "waiting for the shared release %v/%v", deployed.Release.Namespace, deployed.Release.Name)
	}
	return deployed
}

// newKubeWithReadyRelease creates a Kubernetes client and installs or waits for
// the configured release before returning that client. Release cleanup is
// registered before fixture cleanup, so fixtures are removed first.
func newKubeWithReadyRelease(ctx context.Context, t *testing.T) *kube.Client {
	t.Helper()
	started := time.Now()
	k := newKube(t)
	readyRelease(ctx, t, k)
	t.Logf("release ready after %s", time.Since(started))
	return k
}

// releaseRef names the release the suite operates on.
func releaseRef() helm.Release {
	return helm.Release{Name: *release, Namespace: *namespace}
}

// deadline budgets one operation. The grace beyond the timeout is there so
// that a command handed the same timeout reports its own failure, instead of
// being cut off by the context first and reporting a less specific one.
func deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, *timeout+30*time.Second)
}

// registerChartCleanup records ownership before a readiness assertion can
// stop its caller. Cleanup uses independent contexts because a test's context
// is canceled before its cleanup functions run.
func registerChartCleanup(t *testing.T, deployed *framework.Chart, k *kube.Client) {
	t.Helper()
	t.Cleanup(func() {
		uninstallCtx, cancelUninstall := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout+30*time.Second)
		assert.NoError(t, deployed.Uninstall(uninstallCtx), "uninstalling the test release")
		ll, err := k.ListLeases(uninstallCtx, *namespace)
		assert.NoError(t, err, "failed to list leases in namespace")
		if err == nil {
			for _, lease := range ll.Items {
				if strings.HasSuffix(lease.Name, ".opentelemetry.io") {
					assert.NoError(t, k.DeleteLease(uninstallCtx, *namespace, lease.Name), "clearing the Operator's leftover leader-election lease")
				}
			}
		}
		cancelUninstall()
		waitCtx, cancelWait := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout+30*time.Second)
		defer cancelWait()
		assert.NoError(t, k.WaitWorkloadsGone(waitCtx, *namespace), "waiting for the test workloads to be removed")
	})
}

// names lists what a set of custom resources is called.
func names(resources []unstructured.Unstructured) []string {
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		out = append(out, resource.GetName())
	}
	return out
}

// logWriter sends a command's output to the test log, which keeps it with the
// test that ran the command.
type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
