package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestExamplePlans checks examples without contacting a cluster or reading credentials.
func TestExamplePlans(t *testing.T) {
	paths, err := filepath.Glob("../plans/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("find example plans: paths=%v error=%v", paths, err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Parse(data, map[string]string{"run-id": "example-run"})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if filepath.Base(path) == "telemetrygen.yaml" {
				for _, stage := range plan.Stages {
					var spec struct {
						Args []string `yaml:"args"`
					}
					if err := stage.Generator.Spec.Decode(&spec); err != nil {
						t.Fatalf("Decode() error = %v", err)
					}
					want := `--telemetry-attributes=test_run_id="example-run"`
					if !slices.Contains(spec.Args, want) {
						t.Errorf("stage %q args = %v, want attribute %q", stage.Name, spec.Args, want)
					}
					var measurementSpec struct {
						Queries map[string]string `yaml:"queries"`
					}
					if err := stage.Measurement.Spec.Decode(&measurementSpec); err != nil {
						t.Fatalf("Decode measurement spec: %v", err)
					}
					if len(measurementSpec.Queries) == 0 {
						t.Errorf("stage %q has no example queries", stage.Name)
					}
					for name, query := range measurementSpec.Queries {
						if !strings.Contains(query, `"example-run"`) || strings.Contains(query, "{{args.") {
							t.Errorf("stage %q query %q = %q, want resolved run ID", stage.Name, name, query)
						}
					}
					measurement, err := newMeasurement(stage.Measurement)
					if err != nil {
						t.Fatalf("newMeasurement() error = %v", err)
					}
					report, err := measurement.Run(t.Context())
					if err != nil || len(report) != 0 {
						t.Errorf("stage %q measurement Run() = %v, %v, want empty success", stage.Name, report, err)
					}
				}
			}
			if _, err := prepareStages(plan.Stages, "/example/kubeconfig"); err != nil {
				t.Errorf("prepareStages() error = %v", err)
			}
		})
	}
}
