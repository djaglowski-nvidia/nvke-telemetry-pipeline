// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planYAML describes two sequential stages with named no-op generators.
const planYAML = `name: smoke
timeout: 1m
stages:
  - name: first
    generators:
      - {name: load, type: noop, spec: {}}
    measurement: {type: noop, spec: {}}
  - name: second
    generators:
      - {name: load, type: noop, spec: {}}
    measurement: {type: noop, spec: {}}
`

// TestParse checks named components and stage declaration order.
func TestParse(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, plan.Timeout)
	require.Len(t, plan.Stages, 2)
	require.Len(t, plan.Stages[0].Generators, 1)
	assert.Equal(t, "load", plan.Stages[0].Generators[0].Name)
	assert.Equal(t, "noop", plan.Stages[0].Generators[0].Type)
	assert.Equal(t, "second", plan.Stages[1].Name)
}

// TestParseRejectsInvalidDocuments checks syntax, envelope fields and topology.
func TestParseRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "decode plan"},
		{name: "syntax", data: "stages: [", want: "decode plan"},
		{name: "unknown field", data: planYAML + "typo: true\n", want: "field typo"},
		{name: "old generator field", data: strings.Replace(planYAML, "generators:", "generator:", 1), want: "field generator"},
		{name: "duplicate key", data: planYAML + "name: duplicate\n", want: "already defined"},
		{name: "second document", data: planYAML + "---\nname: extra\n", want: "exactly one"},
		{name: "empty trailing document", data: planYAML + "---\n", want: "exactly one"},
		{name: "missing name", data: strings.Replace(planYAML, "name: smoke", "name: ''", 1), want: "name is required"},
		{name: "old execution field", data: planYAML + "execution: parallel\n", want: "field execution"},
		{name: "bad duration", data: strings.Replace(planYAML, "1m", "forever", 1), want: "decode plan"},
		{name: "zero timeout", data: strings.Replace(planYAML, "1m", "0s", 1), want: "timeout must be positive"},
		{name: "no stages", data: "name: empty\ntimeout: 1m\nstages: []\n", want: "at least one stage"},
		{name: "missing stage name", data: strings.Replace(planYAML, "name: first", "name: ''", 1), want: "stages[0].name"},
		{name: "duplicate stage name", data: strings.Replace(planYAML, "name: second", "name: first", 1), want: "duplicate stage"},
		{name: "missing generator name", data: strings.Replace(planYAML, "name: load", "name: ''", 1), want: "generators[0].name"},
		{name: "duplicate generator name", data: strings.Replace(planYAML, "generators:", "generators:\n      - {name: load, type: noop}", 1), want: "duplicate generator name"},
		{name: "missing generator type", data: strings.Replace(planYAML, "type: noop", "type: ''", 1), want: "requires type"},
		{name: "missing measurement type", data: strings.Replace(planYAML, "measurement: {type: noop, spec: {}}", "measurement: {}", 1), want: "measurement.type"},
		{name: "empty stage", data: "name: empty\ntimeout: 1m\nstages: [{name: empty}]\n", want: "requires generators or a measurement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data), nil)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// TestExecuteValidatesComponents checks every reference before running a plan.
func TestExecuteValidatesComponents(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "generator type", data: strings.Replace(planYAML, "name: load, type: noop", "name: load, type: missing", 1), want: `generator "load": unknown type "missing"`},
		{name: "later stage", data: planYAML + "  - name: invalid\n    measurement: {type: missing}\n", want: `stage "invalid" measurement: unknown type "missing"`},
		{name: "extra option", data: strings.Replace(planYAML, "spec: {}", "spec: {rate: 2}", 1), want: "noop spec must be an empty mapping"},
		{name: "scalar spec", data: strings.Replace(planYAML, "spec: {}", "spec: 3", 1), want: "noop spec must be an empty mapping"},
		{name: "null spec", data: strings.Replace(planYAML, "spec: {}", "spec: null", 1), want: "noop spec must be an empty mapping"},
		{name: "measurement extra option", data: strings.Replace(planYAML, "measurement: {type: noop, spec: {}}", "measurement: {type: noop, spec: {rate: 2}}", 1), want: "measurement: noop spec only supports a measurements sequence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse([]byte(tc.data), nil)
			require.NoError(t, err)
			report, err := (Executor{}).Execute(t.Context(), plan, "test-run")
			assert.ErrorContains(t, err, tc.want)
			assert.Equal(t, PlanReport{}, report)
		})
	}
}

// TestExecuteNoopPlan checks report identity and reuse of a parsed plan.
func TestExecuteNoopPlan(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	require.NoError(t, err)
	var previousID string
	for range 2 {
		args, err := ParseArguments(nil)
		require.NoError(t, err)
		report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
		require.NoError(t, err)
		assert.Equal(t, args[runIDArgument], report.RunID)
		assert.NotEqual(t, previousID, report.RunID)
		assert.Equal(t, "smoke", report.Plan)
		previousID = report.RunID
		assert.Equal(t, []StageReport{
			{Name: "first", Generators: []GeneratorReport{{Name: "load"}}, Measurement: "noop", Report: measurements.Report{}},
			{Name: "second", Generators: []GeneratorReport{{Name: "load"}}, Measurement: "noop", Report: measurements.Report{}},
		}, report.Stages)
	}
}

// TestOptionalComponents checks observation-only and load-only stages.
func TestOptionalComponents(t *testing.T) {
	plan, err := Parse([]byte(`name: optional
timeout: 1m
stages:
  - name: baseline
    measurement: {type: noop}
  - name: load
    generators:
      - {name: load, type: noop}
`), nil)
	require.NoError(t, err)
	report, err := (Executor{}).Execute(t.Context(), plan, "test-run")
	require.NoError(t, err)
	require.Len(t, report.Stages, 2)
	assert.Empty(t, report.Stages[0].Generators)
	assert.Equal(t, "noop", report.Stages[0].Measurement)
	assert.Equal(t, []GeneratorReport{{Name: "load"}}, report.Stages[1].Generators)
	assert.Empty(t, report.Stages[1].Measurement)
}

// TestExecuteRejectsInvalidInput checks the public API without relying on parsing.
func TestExecuteRejectsInvalidInput(t *testing.T) {
	_, err := (Executor{}).Execute(t.Context(), Plan{}, "test-run")
	assert.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (Executor{}).Execute(ctx, Plan{}, "test-run")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestExecuteUsesSuppliedRunID checks that reports retain the ID supplied during parsing.
func TestExecuteUsesSuppliedRunID(t *testing.T) {
	args, err := ParseArguments([]string{"run-id=ci-123"})
	require.NoError(t, err)
	plan, err := Parse([]byte(planYAML), args)
	require.NoError(t, err)
	for range 2 {
		report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
		require.NoError(t, err)
		assert.Equal(t, "ci-123", report.RunID)
	}
}

// TestExecuteRejectsInvalidRunID checks execution rejects missing or malformed IDs.
func TestExecuteRejectsInvalidRunID(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	require.NoError(t, err)
	for _, id := range []string{"", "bad id"} {
		_, err := (Executor{}).Execute(t.Context(), plan, id)
		assert.ErrorContains(t, err, "run-id")
	}
}

// TestParseRejectsInvalidRunID checks direct callers cannot bind an invalid ID.
func TestParseRejectsInvalidRunID(t *testing.T) {
	for _, id := range []string{"", "bad id"} {
		t.Run("run-id="+id, func(t *testing.T) {
			_, err := Parse([]byte(planYAML), map[string]string{runIDArgument: id})
			assert.ErrorContains(t, err, "run-id")
		})
	}
}

// TestExecuteRejectsMismatchedRunID checks parsing binds a snapshot of the run ID.
func TestExecuteRejectsMismatchedRunID(t *testing.T) {
	args := map[string]string{runIDArgument: "run-a"}
	plan, err := Parse([]byte(planYAML), args)
	require.NoError(t, err)
	args[runIDArgument] = "run-b"
	report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
	assert.ErrorContains(t, err, "does not match")
	assert.Equal(t, PlanReport{}, report)
	report, err = (Executor{}).Execute(t.Context(), plan, "run-a")
	require.NoError(t, err)
	assert.Equal(t, "run-a", report.RunID)
}
