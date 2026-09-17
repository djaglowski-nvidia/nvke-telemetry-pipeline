package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExamplePlans checks examples without contacting a cluster or reading credentials.
func TestExamplePlans(t *testing.T) {
	paths, err := filepath.Glob("../plans/*.yaml")
	require.NoError(t, err, "find example plans")
	require.NotEmpty(t, paths, "find example plans")
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			plan, err := Parse(data, map[string]string{"run-id": "example-run"})
			require.NoError(t, err, "Parse()")
			if filepath.Base(path) == "telemetrygen.yaml" {
				for _, stage := range plan.Stages {
					var spec struct {
						Args []string `yaml:"args"`
					}
					require.NoError(t, stage.Generator.Spec.Decode(&spec), "Decode()")
					want := `--telemetry-attributes=test_run_id="example-run"`
					assert.Contains(t, spec.Args, want, "stage %q args = %v, want attribute %q", stage.Name, spec.Args, want)
					var measurementSpec struct {
						Queries  map[string]string `yaml:"queries"`
						Endpoint string            `yaml:"endpoint"`
					}
					require.NoError(t, stage.Measurement.Spec.Decode(&measurementSpec), "Decode measurement spec")
					assert.NotEmpty(t, measurementSpec.Queries, "stage %q has no example queries", stage.Name)
					for name, query := range measurementSpec.Queries {
						assert.NotContains(t, query, "{{args.", "stage %q query %q contains unresolved arguments: %q", stage.Name, name, query)
						if stage.Name == "metrics" && name != "active_series" {
							continue
						}
						assert.Contains(t, query, `"example-run"`, "stage %q query %q = %q, want resolved run ID", stage.Name, name, query)
					}
					if stage.Name == "metrics" {
						require.Equal(t, "prometheus", stage.Measurement.Type, "metrics measurement type = %q, want prometheus", stage.Measurement.Type)
						assert.Equal(t, "http://127.0.0.1:9090", measurementSpec.Endpoint, "metrics endpoint = %q, want the scale cluster port-forward URL", measurementSpec.Endpoint)
						assert.Contains(t, measurementSpec.Queries, "active_series", "metrics measurement is missing the attributed series query")
						continue
					}
					measurement, err := newMeasurement(stage.Measurement)
					require.NoError(t, err, "newMeasurement()")
					report, err := measurement.Run(t.Context())
					assert.NoError(t, err, "stage %q measurement Run()", stage.Name)
					assert.Empty(t, report, "stage %q measurement Run() = %v, want empty success", stage.Name, report)
				}
			}
			_, err = prepareStages(plan.Stages, "/example/kubeconfig")
			assert.NoError(t, err, "prepareStages()")
		})
	}
}
