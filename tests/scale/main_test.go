//go:build scale

package scale

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runner "github.com/NVIDIA-dev/nktp/tests/scale/pkg"
)

// planPath selects a YAML plan relative to tests/scale or by absolute path.
var planPath = flag.String("plan", "plans/noop.yaml", "scale test plan file")

// kubeconfigPath selects one file for every Job generator in the plan.
var kubeconfigPath = flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig file (default: tests/scale/.kubeconfig)")

// TestRun loads a plan and logs consolidated reports after every stage completes.
// The initial no-op implementations require neither Kubernetes nor a load backend.
func TestRun(t *testing.T) {
	kubeconfig, err := resolveKubeconfig(*kubeconfigPath, flag.Args())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(*planPath)
	if err != nil {
		t.Fatalf("read plan %q: %v", *planPath, err)
	}
	plan, err := runner.Parse(data)
	if err != nil {
		t.Fatalf("parse plan %q: %v", *planPath, err)
	}
	report, err := (runner.Executor{Kubeconfig: kubeconfig}).Execute(t.Context(), plan)
	t.Logf("plan=%s run=%s stages=%d", report.Plan, report.RunID, len(report.Stages))
	for _, stage := range report.Stages {
		t.Logf("stage=%s measurement=%s report=%v generator_error=%v measurement_error=%v",
			stage.Name, stage.Measurement, stage.Report, stage.GeneratorError, stage.MeasurementError)
	}
	if err != nil {
		t.Errorf("execute plan %q: %v", plan.Name, err)
	}
}

// resolveKubeconfig also accepts the trailing KUBECONFIG=path form forwarded by
// Task after --. Other positional arguments and multi-file values are errors.
func resolveKubeconfig(path string, args []string) (string, error) {
	for _, arg := range args {
		value, ok := strings.CutPrefix(arg, "KUBECONFIG=")
		if !ok || value == "" {
			return "", fmt.Errorf("unexpected argument %q: use -kubeconfig PATH or KUBECONFIG=PATH", arg)
		}
		path = value
	}
	if path == "" {
		// Go runs this package's tests from tests/scale.
		path = ".kubeconfig"
	}
	if paths := filepath.SplitList(path); len(paths) > 1 {
		return "", fmt.Errorf("kubeconfig %q lists multiple files: pass one path", path)
	}
	return filepath.Abs(path)
}
