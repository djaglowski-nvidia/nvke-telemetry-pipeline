// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Measurement names a query and its optional display units.
type Measurement struct {
	Name  string `yaml:"name"`
	Units string `yaml:"units,omitempty"`
	Query string `yaml:"query"`
}

// validateMeasurements checks entry fields before decoding can coerce scalar types.
// Names are unique within the component, and omitted units leave values unscaled.
func validateMeasurements(node yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return errors.New("measurements must be a sequence")
	}
	names := make(map[string]bool, len(node.Content))
	for i, entry := range node.Content {
		if entry.Kind != yaml.MappingNode {
			return fmt.Errorf("measurements[%d] must be a mapping", i)
		}
		fields := make(map[string]string, 3)
		for j := 0; j < len(entry.Content); j += 2 {
			key, value := entry.Content[j], entry.Content[j+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("measurements[%d] field names must be strings", i)
			}
			switch key.Value {
			case "name", "units", "query":
			default:
				return fmt.Errorf("measurements[%d] has unknown field %q", i, key.Value)
			}
			if _, ok := fields[key.Value]; ok {
				return fmt.Errorf("measurements[%d] has duplicate field %q", i, key.Value)
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
				return fmt.Errorf("measurements[%d].%s must be a nonempty string", i, key.Value)
			}
			fields[key.Value] = value.Value
		}
		name := fields["name"]
		if name == "" || fields["query"] == "" {
			return fmt.Errorf("measurements[%d] requires name and query", i)
		}
		if names[name] {
			return fmt.Errorf("duplicate measurement name %q", name)
		}
		names[name] = true
	}
	return nil
}
