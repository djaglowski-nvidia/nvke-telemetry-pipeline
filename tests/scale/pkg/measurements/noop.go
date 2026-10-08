// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"context"
	"errors"

	"go.yaml.in/yaml/v3"
)

// Noop exercises report collection without observing a system.
type Noop struct{}

// NewNoop validates the spec and constructs a measurement without acquiring resources.
// The optional measurements sequence holds named examples. Queries are neither
// parsed as query languages nor executed, and they do not produce findings.
// Checks are rejected because this implementation cannot enforce them.
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

// validateNoopSpec accepts an omitted spec, an empty mapping or example measurements.
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
	if len(spec.Content) != 2 || spec.Content[0].Tag != "!!str" || spec.Content[0].Value != "measurements" {
		return errors.New("noop spec only supports a measurements sequence")
	}
	if err := validateMeasurements(*spec.Content[1]); err != nil {
		return err
	}
	var entries []Measurement
	if err := spec.Content[1].Decode(&entries); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Check != nil {
			return errors.New("noop measurements do not support checks")
		}
	}
	return nil
}
