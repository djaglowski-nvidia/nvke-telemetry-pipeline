// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const runIDArgument = "runId"

var (
	// argumentNamePattern defines valid names for flags and argument references.
	argumentNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(-[A-Za-z0-9_]+)*$`)
	// runIDPattern limits run IDs to 1-63 ASCII characters with alphanumeric ends.
	runIDPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)
)

func ParseArguments(values []string) (map[string]string, error) {
	args := make(map[string]string, len(values)+1)
	for _, assignment := range values {
		name, value, ok := strings.Cut(assignment, "=")
		if !ok || !argumentNamePattern.MatchString(name) {
			return nil, errors.New("argument must have the form NAME=VALUE with a valid name")
		}
		key := toCamelCase(name)
		if _, exists := args[key]; exists {
			return nil, fmt.Errorf("duplicate argument %q", name)
		}
		args[key] = value
	}
	if _, supplied := args[runIDArgument]; !supplied {
		args[runIDArgument] = envconf.RandomName("", 32)
	}
	if err := validateRunID(args[runIDArgument]); err != nil {
		return nil, err
	}
	return args, nil
}

func toCamelCase(name string) string {
	parts := strings.Split(name, "-")
	var normalized strings.Builder
	normalized.WriteString(parts[0])
	for _, part := range parts[1:] {
		first := part[0]
		if first >= 'a' && first <= 'z' {
			first -= 'a' - 'A'
		}
		normalized.WriteByte(first)
		normalized.WriteString(part[1:])
	}
	return normalized.String()
}

// validateRunID rejects IDs that do not match the shared attribution format.
func validateRunID(id string) error {
	if !runIDPattern.MatchString(id) {
		return errors.New("run-id must be 1-63 ASCII characters, start and end with a letter or digit, and contain only letters, digits, underscores, hyphens or dots")
	}
	return nil
}
