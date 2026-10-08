// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package generators

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
)

// TestAvalanchePlan preserves workload settings in the shipped plan.
func TestAvalanchePlan(t *testing.T) {
	data, err := os.ReadFile("../../plans/avalanche.yaml")
	require.NoError(t, err)
	var plan struct {
		Stages []struct {
			Generators []struct {
				Type string    `yaml:"type"`
				Spec yaml.Node `yaml:"spec"`
			} `yaml:"generators"`
		} `yaml:"stages"`
	}
	require.NoError(t, yaml.Unmarshal(data, &plan))
	require.NotEmpty(t, plan.Stages)
	require.Len(t, plan.Stages[0].Generators, 1)
	assert.Equal(t, "statefulset", plan.Stages[0].Generators[0].Type)
	g, err := NewStatefulSetGenerator(plan.Stages[0].Generators[0].Spec, "/does/not/exist")
	require.NoError(t, err)
	assert.Equal(t, "avalanche", g.config.PodTemplate.Labels["scale.nktp.io/workload"])
	require.NotEmpty(t, g.config.PodTemplate.Spec.Containers)
	container := g.config.PodTemplate.Spec.Containers[0]
	// The generator controls lifetime and Pod names provide stable replica identity.
	assert.Equal(t, []string{"/bin/avalanche"}, container.Command)
	assert.Contains(t, container.Args, "--const-label=generator_instance=$(POD_NAME)")
	assert.Contains(t, container.Env, corev1.EnvVar{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
		FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
	}})
}
