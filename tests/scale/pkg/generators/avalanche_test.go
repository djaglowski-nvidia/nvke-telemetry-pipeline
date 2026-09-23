//go:build scale

package generators

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
)

// TestAvalanchePlan checks the workload lifetime and stable replica attribution.
func TestAvalanchePlan(t *testing.T) {
	// data is the actual plan used by task test:scale.
	data, err := os.ReadFile("../../plans/avalanche.yaml")
	if err != nil {
		t.Fatalf("read Avalanche plan: %v", err)
	}
	// plan retains generator YAML for native Kubernetes parsing.
	var plan struct {
		// Stages contains the Avalanche workload.
		Stages []struct {
			// Generator selects the constructor and its specification.
			Generator struct {
				// Type must select the StatefulSet generator.
				Type string `yaml:"type"`
				// Spec preserves native Pod field names for the constructor.
				Spec yaml.Node `yaml:"spec"`
			} `yaml:"generator"`
		} `yaml:"stages"`
	}
	err = yaml.Unmarshal(data, &plan)
	if err != nil {
		t.Fatalf("decode Avalanche plan: %v", err)
	}
	if len(plan.Stages) != 1 {
		t.Fatalf("Avalanche stages = %d, want 1", len(plan.Stages))
	}
	if plan.Stages[0].Generator.Type != "statefulset" {
		t.Errorf("Avalanche generator = %q, want statefulset", plan.Stages[0].Generator.Type)
	}
	// g checks constructor compatibility without requiring cluster credentials.
	g, err := NewStatefulSetGenerator(plan.Stages[0].Generator.Spec, "/does/not/exist")
	if err != nil {
		t.Fatalf("NewStatefulSetGenerator(): %v", err)
	}
	if g.config.Replicas != 3 || g.config.Duration.Duration != 3*time.Minute {
		t.Errorf("Avalanche replicas and duration = (%d, %v), want (3, 3m)", g.config.Replicas, g.config.Duration.Duration)
	}
	if g.config.PodTemplate.Labels["scale.nktp.io/workload"] != "avalanche" {
		t.Errorf("Avalanche labels = %v, want scrape discovery label", g.config.PodTemplate.Labels)
	}
	if len(g.config.PodTemplate.Spec.Containers) != 1 {
		t.Fatalf("Avalanche containers = %d, want 1", len(g.config.PodTemplate.Spec.Containers))
	}
	// container must launch the binary directly and let the generator own duration.
	container := g.config.PodTemplate.Spec.Containers[0]
	// commandDiff shows any reintroduction of the finite shell wrapper.
	commandDiff := cmp.Diff([]string{"/bin/avalanche"}, container.Command)
	if commandDiff != "" {
		t.Errorf("Avalanche command mismatch (-want +got):\n%s", commandDiff)
	}
	if !slices.Contains(container.Args, "--const-label=generator_instance=$(POD_NAME)") {
		t.Errorf("Avalanche args = %v, want stable generator_instance label", container.Args)
	}
	// wantEnv uses the Pod name that survives StatefulSet replica replacement.
	wantEnv := []corev1.EnvVar{{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
		FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
	}}}
	// envDiff identifies changes to the Downward API identity mapping.
	envDiff := cmp.Diff(wantEnv, container.Env)
	if envDiff != "" {
		t.Errorf("Avalanche env mismatch (-want +got):\n%s", envDiff)
	}
}
