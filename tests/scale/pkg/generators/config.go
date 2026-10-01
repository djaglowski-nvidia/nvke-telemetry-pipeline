package generators

import (
	"fmt"

	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	k8syaml "sigs.k8s.io/yaml"
)

// generatorOwnerLabel identifies the invocation that owns a generated resource.
const generatorOwnerLabel = "scale.nktp.io/generator"

// decodeSpec uses Kubernetes JSON field names and rejects unknown or duplicate
// fields. The caller supplies a pointer to a config with its defaults applied.
func decodeSpec(spec yaml.Node, into any) error {
	data, err := yaml.Marshal(&spec)
	if err != nil {
		return fmt.Errorf("encode spec: %w", err)
	}
	return k8syaml.UnmarshalStrict(data, into)
}

// preparePodTemplate copies native fields before adding per-run ownership and
// a default namespace. It leaves the caller's template unchanged.
func preparePodTemplate(src corev1.PodTemplateSpec, owner string) corev1.PodTemplateSpec {
	template := src.DeepCopy()
	if template.Namespace == "" {
		template.Namespace = "default"
	}
	if template.Labels == nil {
		template.Labels = make(map[string]string)
	}
	template.Labels[generatorOwnerLabel] = owner
	return *template
}
