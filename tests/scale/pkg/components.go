package runner

import (
	"fmt"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/generators"
	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
)

// newGenerator selects a generator constructor that validates its spec.
func newGenerator(config ComponentSpec, kubeconfig string) (generator, error) {
	switch config.Type {
	case "noop":
		return generators.NewNoop(config.Spec)
	case "kubernetes":
		return generators.NewKubernetesGenerator(config.Spec, kubeconfig)
	case "pod":
		return generators.NewPodGenerator(config.Spec, kubeconfig)
	case "statefulset":
		return generators.NewStatefulSetGenerator(config.Spec, kubeconfig)
	case "job":
		return generators.NewJobGenerator(config.Spec, kubeconfig)
	default:
		return nil, fmt.Errorf("unknown type %q", config.Type)
	}
}

// newMeasurement selects a measurement constructor that validates its spec.
func newMeasurement(config ComponentSpec) (measurement, error) {
	switch config.Type {
	case "noop":
		return measurements.NewNoop(config.Spec)
	case "prometheus":
		return measurements.NewPrometheus(config.Spec)
	default:
		return nil, fmt.Errorf("unknown type %q", config.Type)
	}
}
