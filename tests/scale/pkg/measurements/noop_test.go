// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestNoopAcceptsQueriesWithoutEvaluatingThem checks query examples produce no findings.
func TestNoopAcceptsQueriesWithoutEvaluatingThem(t *testing.T) {
	for _, spec := range []string{
		"",
		"{}",
		"measurements: []",
		"measurements:\n  - name: example\n    query: deliberately invalid query syntax\n",
		"measurements:\n  - name: example\n    units: bytes\n    query: deliberately invalid query syntax\n",
	} {
		t.Run(spec, func(t *testing.T) {
			var node yaml.Node
			if spec != "" {
				node = measurementSpec(t, spec)
			}
			measurement, err := NewNoop(node)
			require.NoError(t, err)
			report, err := measurement.Run(t.Context())
			require.NoError(t, err)
			assert.Empty(t, report)
		})
	}
}

// TestNoopRejectsInvalidSpecs checks the shape and string values of measurements.
func TestNoopRejectsInvalidSpecs(t *testing.T) {
	for _, spec := range []string{
		"null",
		"[]",
		"rate: 1",
		"queries: {example: query}",
		"measurements:\n  - name: unavailable\n    check: {expr: up == 0}",
		"measurements: null",
		"measurements: {}",
		"measurements: []\nextra: true",
		"measurements: []\nmeasurements: []",
		"measurements:\n  - name: ''\n    query: up",
		"measurements:\n  - name: 1\n    query: up",
		"measurements:\n  - name: true\n    query: up",
		"measurements:\n  - ''",
		"measurements:\n  - {}",
		"measurements:\n  - name: example\n    query: ''",
		"measurements:\n  - name: example\n    query: ' '",
		"measurements:\n  - name: example\n    query: 100",
		"measurements:\n  - name: example\n    query: true",
		"measurements:\n  - name: example\n    query: null",
		"measurements:\n  - name: example\n    query: []",
		"measurements:\n  - name: example\n    query: first\n  - name: example\n    query: second",
		"measurements:\n  - name: example\n    name: second\n    query: up",
		"measurements:\n  - name: example\n    query: first\n    query: second",
		"measurements:\n  - name: example\n    query: up\n    units: null",
		"measurements:\n  - name: first\n    query: &query up\n  - name: second\n    query: *query",
		"measurements:\n  - name: example\n    <<: {query: up}",
	} {
		t.Run(spec, func(t *testing.T) {
			_, err := NewNoop(measurementSpec(t, spec))
			assert.Error(t, err)
		})
	}
}
