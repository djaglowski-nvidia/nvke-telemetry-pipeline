//go:build scale

package scale

import (
	"flag"
	"os"
	"testing"

	runner "github.com/NVIDIA-dev/nktp/tests/scale/pkg"
)

// planPath selects a YAML plan relative to tests/scale or by absolute path.
var planPath = flag.String("plan", "plans/noop.yaml", "scale test plan file")

// TestRun loads a plan and logs consolidated reports after every stage completes.
// The initial no-op implementations require neither Kubernetes nor a load backend.
func TestRun(t *testing.T) {
	data, err := os.ReadFile(*planPath)
	if err != nil {
		t.Fatalf("read plan %q: %v", *planPath, err)
	}
	plan, err := runner.Parse(data)
	if err != nil {
		t.Fatalf("parse plan %q: %v", *planPath, err)
	}
	report, err := (runner.Executor{}).Execute(t.Context(), plan)
	t.Logf("plan=%s run=%s stages=%d", report.Plan, report.RunID, len(report.Stages))
	for _, stage := range report.Stages {
		t.Logf("stage=%s measurement=%s report=%v generator_error=%v measurement_error=%v",
			stage.Name, stage.Measurement, stage.Report, stage.GeneratorError, stage.MeasurementError)
	}
	if err != nil {
		t.Errorf("execute plan %q: %v", plan.Name, err)
	}
}
