package measurements

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Noop exercises report collection without observing a system.
type Noop struct{}

// NewNoop validates the spec and constructs a measurement without acquiring resources.
// The optional queries mapping holds named example strings. Queries are neither
// parsed as query languages nor executed, and they do not produce findings.
func NewNoop(spec yaml.Node) (Noop, error) {
	if err := validateNoopSpec(spec); err != nil {
		return Noop{}, err
	}
	return Noop{}, nil
}

// Run returns an empty successful report.
func (Noop) Run(context.Context) (Report, error) {
	return Report{}, nil
}

// validateNoopSpec accepts an omitted spec, an empty mapping or named queries.
func validateNoopSpec(spec yaml.Node) error {
	if spec.Kind == 0 {
		return nil
	}
	if spec.Kind != yaml.MappingNode {
		return errors.New("noop spec must be a mapping")
	}
	if len(spec.Content) == 0 {
		return nil
	}
	if len(spec.Content) != 2 || spec.Content[0].Tag != "!!str" || spec.Content[0].Value != "queries" {
		return errors.New("noop spec only supports a queries mapping")
	}
	queries := spec.Content[1]
	if queries.Kind != yaml.MappingNode {
		return errors.New("queries must be a mapping of names to query strings")
	}
	seen := make(map[string]bool)
	for i := 0; i < len(queries.Content); i += 2 {
		name, query := queries.Content[i], queries.Content[i+1]
		if name.Kind != yaml.ScalarNode || name.Tag != "!!str" || strings.TrimSpace(name.Value) == "" {
			return errors.New("query names must be nonempty strings")
		}
		if seen[name.Value] {
			return fmt.Errorf("duplicate query name %q", name.Value)
		}
		seen[name.Value] = true
		if query.Kind != yaml.ScalarNode || query.Tag != "!!str" || strings.TrimSpace(query.Value) == "" {
			return fmt.Errorf("query %q must be a nonempty string", name.Value)
		}
	}
	return nil
}
