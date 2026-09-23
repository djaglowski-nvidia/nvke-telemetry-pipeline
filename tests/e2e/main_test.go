//go:build e2e

// Package e2e installs the NKTP chart on a cluster and asserts what a user can
// observe of the result.
//
// The suite is handed a cluster and installs the chart itself, because the
// install is the thing under test. `task e2e:test` creates a throwaway cluster
// and fetches the chart's dependencies first; pointing the suite at any other
// cluster is a matter of the flags below.
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
		"install the chart and remove it again; unset to validate a release already on the cluster")
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

// newChart returns the reusable installer for the release this suite owns.
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

// releaseRef names the release the suite operates on.
func releaseRef() helm.Release {
	return helm.Release{Name: *release, Namespace: *namespace}
}

// deadline budgets one operation. The grace beyond the timeout is there so
// that a command handed the same timeout reports its own failure, instead of
// being cut off by the context first and reporting a less specific one.
func deadline(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), *timeout+30*time.Second)
}

// registerChartCleanup records ownership before a readiness assertion can
// stop its caller. Cleanup uses independent contexts because a test's context
// is canceled before its cleanup functions run.
func registerChartCleanup(t *testing.T, deployed *framework.Chart, k *kube.Client) {
	t.Helper()
	t.Cleanup(func() {
		uninstallCtx, cancelUninstall := context.WithTimeout(context.Background(), *timeout+30*time.Second)
		assert.NoError(t, deployed.Uninstall(uninstallCtx), "uninstalling the test release")
		cancelUninstall()

		waitCtx, cancelWait := context.WithTimeout(context.Background(), *timeout+30*time.Second)
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
