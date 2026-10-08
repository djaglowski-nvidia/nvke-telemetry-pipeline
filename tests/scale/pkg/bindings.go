// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"strings"
	"text/template"
)

func bindArguments(data string, args map[string]string) (string, error) {
	tmpl, err := template.New("plan").Funcs(template.FuncMap{
		"index": indexArgument,
	}).Option("missingkey=error").Parse(data)
	if err != nil {
		return "", fmt.Errorf("parse plan template: %w", err)
	}
	var resolved strings.Builder
	if err := tmpl.Execute(&resolved, struct {
		Args map[string]string
	}{Args: args}); err != nil {
		return "", fmt.Errorf("execute plan template: %w", err)
	}
	return resolved.String(), nil
}

func indexArgument(args map[string]string, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("map has no entry for key %q", key)
	}
	return value, nil
}
