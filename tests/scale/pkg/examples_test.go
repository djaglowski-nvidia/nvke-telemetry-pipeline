package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
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
			plan, err := Parse(data, map[string]string{runIDArgument: "example-run"})
			require.NoError(t, err, "Parse()")
			if filepath.Base(path) == "telemetrygen.yaml" {
				for _, stage := range plan.Stages {
					var spec struct {
						PodTemplate struct {
							Spec struct {
								Containers []struct {
									Args []string `yaml:"args"`
								} `yaml:"containers"`
							} `yaml:"spec"`
						} `yaml:"podTemplate"`
					}
					require.NoError(t, stage.Generator.Spec.Decode(&spec), "Decode()")
					want := `--telemetry-attributes=test_run_id="example-run"`
					containers := spec.PodTemplate.Spec.Containers
					require.Len(t, containers, 1, "stage %q containers", stage.Name)
					assert.Contains(t, containers[0].Args, want, "stage %q telemetry attribute", stage.Name)
					if stage.Name == "metrics" {
						require.Equal(t, "prometheus", stage.Measurement.Type)
						var spec struct {
							Measurements []measurements.Measurement `yaml:"measurements"`
						}
						require.NoError(t, stage.Measurement.Spec.Decode(&spec))
						index := slices.IndexFunc(spec.Measurements, func(m measurements.Measurement) bool {
							return m.Name == "active_series"
						})
						require.GreaterOrEqual(t, index, 0, "stage %q active_series measurement", stage.Name)
						assert.Contains(t, spec.Measurements[index].Query, `test_run_id="example-run"`, "stage %q measurement attribution", stage.Name)
					}
				}
			}
			_, err = prepareStages(plan.Stages, "/example/kubeconfig")
			assert.NoError(t, err, "prepareStages()")
		})
	}
}
