// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package generators provides workload generators for scale test plans.
package generators

import (
	"context"
	"errors"

	"go.yaml.in/yaml/v3"
)

// Noop exercises generation without creating a workload.
type Noop struct{}

// NewNoop validates the spec and constructs a generator without acquiring resources.
func NewNoop(spec yaml.Node) (Noop, error) {
	if err := validateNoopSpec(spec); err != nil {
		return Noop{}, err
	}
	return Noop{}, nil
}

// Run does no work and returns success.
func (Noop) Run(context.Context) error {
	return nil
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
