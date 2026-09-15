package measurements

import (
	"context"
	"errors"

	"go.yaml.in/yaml/v3"
)

// Noop exercises report collection without observing a system.
type Noop struct{}

// NewNoop validates the spec and constructs a measurement without acquiring resources.
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

// validateNoopSpec accepts an omitted spec or an empty mapping; no options exist.
func validateNoopSpec(spec yaml.Node) error {
	if spec.Kind == 0 {
		return nil
	}
	if spec.Kind != yaml.MappingNode || len(spec.Content) != 0 {
		return errors.New("noop spec must be an empty mapping")
	}
	return nil
}
