// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// TestParseSubstitutesArguments checks template rendering into a plan without changing inputs.
func TestParseSubstitutesArguments(t *testing.T) {
	data := []byte(`name: 'experiment-{{ .Args.name }}'
timeout: '{{ .Args.duration }}'
stages:
  - name: '{{ .Args.stage }}'
    generators:
    - name: load
      type: '{{ .Args.generator }}'
      spec:
        args:
          - '--telemetry-attributes=test_run_id="{{ .Args.runId }}"'
        nested:
          count: 42
          enabled: true
          empty: null
    measurement:
      type: custom
      spec:
        query: 'sum(metric{test_run_id="{{ .Args.runId }}"})'
`)
	args := map[string]string{
		"name":      "smoke",
		"duration":  "1m",
		"stage":     "load",
		"generator": "custom",
		"runId":     "ci-123",
	}
	originalData, originalArgs := bytes.Clone(data), maps.Clone(args)
	plan, err := Parse(data, args)
	require.NoError(t, err)
	assert.Equal(t, "experiment-smoke", plan.Name)
	assert.Equal(t, time.Minute, plan.Timeout)
	require.Len(t, plan.Stages, 1)
	stage := plan.Stages[0]
	assert.Equal(t, "load", stage.Name)
	require.Len(t, stage.Generators, 1)
	assert.Equal(t, "custom", stage.Generators[0].Type)
	var got map[string]any
	require.NoError(t, stage.Generators[0].Spec.Decode(&got))
	assert.Equal(t, map[string]any{
		"args":   []any{`--telemetry-attributes=test_run_id="ci-123"`},
		"nested": map[string]any{"count": 42, "enabled": true, "empty": nil},
	}, got)
	var measurement map[string]string
	require.NoError(t, stage.Measurement.Spec.Decode(&measurement))
	assert.Equal(t, `sum(metric{test_run_id="ci-123"})`, measurement["query"])
	assert.Equal(t, originalData, data)
	assert.Equal(t, originalArgs, args)
}

// TestParseReportsMissingArgument checks that execution rejects an absent value.
func TestParseReportsMissingArgument(t *testing.T) {
	const data = `name: missing-inputs
timeout: 1m
stages:
  - name: load
    generators:
    - name: load
      type: custom
      spec:
        args:
          - '{{ .Args.gateway }}'
          - '{{ .Args.rate }}'
          - '{{ .Args.runId }}'
          - '{{ .Args.gateway }}'
    measurement: {type: noop}
`
	args := map[string]string{"unrelated": "kept"}
	_, err := Parse([]byte(data), args)
	if err == nil {
		t.Fatal("Parse() succeeded with missing arguments")
	}
	if want := `map has no entry for key "gateway"`; !strings.Contains(err.Error(), want) {
		t.Errorf("Parse() error = %q, want error containing %q", err, want)
	}
	if !maps.Equal(args, map[string]string{"unrelated": "kept"}) {
		t.Errorf("Parse() changed arguments to %v, want the original map without a generated runId", args)
	}
}

func TestBindArgumentsIndex(t *testing.T) {
	const data = `{{ index .Args "gateway" }}`

	got, err := bindArguments(data, map[string]string{"gateway": "collector:4317"})
	if err != nil {
		t.Fatalf("bindArguments() error = %v", err)
	}
	if got != "collector:4317" {
		t.Errorf("bindArguments() = %q, want %q", got, "collector:4317")
	}

	_, err = bindArguments(data, map[string]string{})
	if err == nil {
		t.Fatal("bindArguments() succeeded with a missing indexed argument")
	}
	if want := `map has no entry for key "gateway"`; !strings.Contains(err.Error(), want) {
		t.Errorf("bindArguments() error = %q, want error containing %q", err, want)
	}
}

// TestExecuteRejectsInvalidComponentYAML checks component validation after substitution.
func TestExecuteRejectsInvalidComponentYAML(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
	}{
		{name: "resolved unknown key", spec: "{'{{ .Args.name }}': value}"},
		{name: "numeric key", spec: "{1: value}"},
		{name: "Boolean key", spec: "{true: value}"},
		{name: "sequence key", spec: "{[first, second]: value}"},
		{name: "duplicate key", spec: "{image: first, image: second}"},
		{name: "nested duplicate key", spec: "{nested: {value: first, value: second}}"},
		{name: "alias", spec: "{value: &value literal, other: *value}"},
		{name: "merge", spec: "{<<: {value: literal}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(planYAML, "spec: {}", "spec: "+tc.spec, 1)
			plan, err := Parse([]byte(data), map[string]string{"name": "provided"})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if _, err := (Executor{}).Execute(t.Context(), plan, "test-run"); err == nil {
				t.Errorf("Execute() accepted invalid component spec %q", tc.spec)
			}
		})
	}
}

// TestParseSubstitutesBeforeYAMLDecoding checks that values follow the surrounding YAML syntax.
func TestParseSubstitutesBeforeYAMLDecoding(t *testing.T) {
	data := strings.Replace(planYAML, "spec: {}", "spec: { '{{ .Args.key }}': {{ .Args.value }} }", 1)
	plan, err := Parse([]byte(data), map[string]string{"key": "count", "value": "100"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	var got map[string]any
	if err := plan.Stages[0].Generators[0].Spec.Decode(&got); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if want := map[string]any{"count": 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("resolved spec = %#v, want %#v", got, want)
	}

	data = strings.Replace(planYAML, "name: smoke", "name: '{{ .Args.name }}'", 1)
	if _, err := Parse([]byte(data), map[string]string{"name": "O'Reilly"}); err == nil {
		t.Error("Parse() accepted a value that breaks YAML quoting")
	}
	plan, err = Parse([]byte(data), map[string]string{"name": "O''Reilly"})
	if err != nil {
		t.Fatalf("Parse() with YAML-escaped quote error = %v", err)
	}
	if plan.Name != "O'Reilly" {
		t.Errorf("Parse().Name = %q, want O'Reilly", plan.Name)
	}
}

// TestParsePreservesStrictDecoding checks envelope validation and quoted scalar types.
func TestParsePreservesStrictDecoding(t *testing.T) {
	data := strings.Replace(planYAML, "name: smoke", "name: '{{ .Args.name }}'", 1)
	if _, err := Parse([]byte(data+"unknown: true\n"), map[string]string{"name": "resolved"}); err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Errorf("Parse() error = %v, want unknown envelope field error after substitution", err)
	}
	data = strings.Replace(planYAML, "spec: {}", "spec: {count: '{{ .Args.count }}', enabled: '{{ .Args.enabled }}'}", 1)
	plan, err := Parse([]byte(data), map[string]string{"count": "100", "enabled": "true"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	var got map[string]any
	if err := plan.Stages[0].Generators[0].Spec.Decode(&got); err != nil {
		t.Fatalf("decode generator spec: %v", err)
	}
	if want := map[string]any{"count": "100", "enabled": "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("substituted scalar types = %#v, want %#v", got, want)
	}
	var typed struct {
		Count int `yaml:"count"`
	}
	if err := plan.Stages[0].Generators[0].Spec.Decode(&typed); err == nil {
		t.Error("substituted string decoded into an integer, want a type error")
	}
}

// TestParseConcurrentSharedInputs checks independent plans parsed from shared inputs.
func TestParseConcurrentSharedInputs(t *testing.T) {
	data := []byte(strings.Replace(planYAML, "spec: {}", "spec: {value: '{{ .Args.runId }}'}", 1))
	args := map[string]string{runIDArgument: "shared-input", "unused": "unchanged"}
	originalData, originalArgs := bytes.Clone(data), maps.Clone(args)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			plan, err := Parse(data, args)
			if err != nil {
				t.Errorf("Parse() error = %v", err)
				return
			}
			spec := &plan.Stages[0].Generators[0].Spec
			if got := spec.Content[1].Value; got != "shared-input" {
				t.Errorf("resolved value = %q, want shared-input", got)
			}
			// A caller may change its parsed configuration without affecting other loads.
			spec.Content[1].Value = "modified by caller"
		})
	}
	wg.Wait()
	if !bytes.Equal(data, originalData) || !maps.Equal(args, originalArgs) {
		t.Error("concurrent parsing changed shared source or arguments")
	}
}

// TestConcurrentExecuteUsesResolvedJobArguments checks attribution against a fake
// API that captures Job arguments and rejects creation without needing a cluster.
func TestConcurrentExecuteUsesResolvedJobArguments(t *testing.T) {
	const executions = 4
	created := make(chan []string, executions)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/apis/batch/v1/namespaces/load/jobs" {
			var job batchv1.Job
			data, err := io.ReadAll(r.Body)
			if err == nil {
				err = runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), data, &job)
			}
			if err != nil || len(job.Spec.Template.Spec.Containers) != 1 {
				t.Errorf("decode Job: %v, containers=%d", err, len(job.Spec.Template.Spec.Containers))
			} else {
				created <- job.Spec.Template.Spec.Containers[0].Args
			}
			w.WriteHeader(http.StatusForbidden)
			if err := json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonForbidden, Code: 403}); err != nil {
				t.Errorf("Encode forbidden response: %v", err)
			}
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/apis/batch/v1/namespaces/load/jobs/") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		if err := json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404}); err != nil {
			t.Errorf("Encode not-found response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: scale\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: scale\n  context:\n    cluster: test\n", server.URL)
	if err := os.WriteFile(kubeconfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte(`name: concurrent-attribution
timeout: 10s
stages:
  - name: first
    generators:
    - name: load
      type: job
      spec:
        podTemplate:
          metadata:
            namespace: '{{ .Args.namespace }}'
          spec:
            containers:
              - name: generator
                image: example:test
                args:
                  - '{{ .Args.runId }}'
                  - '--telemetry-attributes=test_run_id="{{ .Args.runId }}"'
                  - '{{ .Args.message }}'
                  - '${OTHER} $RUN_ID $(echo untouched)'
    measurement: {type: noop}
`)
	flags := []string{"namespace=load", "message={{ .Args.keep-literal }}"}
	originalData, originalFlags := bytes.Clone(data), slices.Clone(flags)
	reports := make([]PlanReport, executions)
	var wg sync.WaitGroup
	for i := range executions {
		wg.Go(func() {
			inputs := slices.Clone(flags)
			if i%2 == 1 {
				inputs = append(inputs, fmt.Sprintf("run-id=ci-%d", i))
			}
			args, err := ParseArguments(inputs)
			if err != nil {
				t.Errorf("ParseArguments() error = %v", err)
				return
			}
			originalArgs := maps.Clone(args)
			plan, err := Parse(data, args)
			if err != nil {
				t.Errorf("Parse() error = %v", err)
				return
			}
			second := plan.Stages[0]
			second.Name = "second"
			plan.Stages = append(plan.Stages, second)
			before, err := yaml.Marshal(plan)
			if err != nil {
				t.Errorf("marshal plan before execution: %v", err)
				return
			}
			executor := Executor{Kubeconfig: kubeconfig}
			report, err := executor.Execute(t.Context(), plan, "different-run")
			if err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Errorf("Execute() error = %v, want a run ID mismatch before contacting the API", err)
				return
			}
			if !reflect.DeepEqual(report, PlanReport{}) {
				t.Errorf("Execute() report = %+v, want no execution on mismatch", report)
			}
			report, err = executor.Execute(t.Context(), plan, args[runIDArgument])
			if err == nil || !strings.Contains(err.Error(), "create Job") {
				t.Errorf("Execute() error = %v, want fake API rejection", err)
			}
			reports[i] = report
			if report.RunID != args[runIDArgument] {
				t.Errorf("report RunID = %q, want resolved argument %q", report.RunID, args[runIDArgument])
			}
			if i%2 == 1 && report.RunID != fmt.Sprintf("ci-%d", i) {
				t.Errorf("report RunID = %q, want supplied ID ci-%d", report.RunID, i)
			}
			after, err := yaml.Marshal(plan)
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("execution changed the parsed plan, marshal error = %v", err)
			}
			if !maps.Equal(args, originalArgs) {
				t.Error("parsing or execution changed the resolved arguments")
			}
		})
	}
	wg.Wait()
	close(created)
	byRun := make(map[string][][]string)
	for args := range created {
		if len(args) != 4 {
			t.Errorf("Job args = %v, want four resolved arguments", args)
			continue
		}
		byRun[args[0]] = append(byRun[args[0]], args)
	}
	if len(byRun) != executions {
		t.Fatalf("received Jobs for %d run IDs, want %d", len(byRun), executions)
	}
	seen := make(map[string]bool)
	for _, report := range reports {
		if report.RunID == "" || seen[report.RunID] {
			t.Errorf("report RunID = %q, want a nonempty unique ID", report.RunID)
		}
		seen[report.RunID] = true
		// The first stage fails, so the second must never submit its Job.
		require.Len(t, report.Stages, 1)
		require.Len(t, report.Stages[0].Generators, 1)
		assert.Error(t, report.Stages[0].Generators[0].Error)
		jobs := byRun[report.RunID]
		require.Len(t, jobs, 1)
		want := []string{report.RunID, `--telemetry-attributes=test_run_id="` + report.RunID + `"`, "{{ .Args.keep-literal }}", "${OTHER} $RUN_ID $(echo untouched)"}
		for _, got := range jobs {
			if !slices.Equal(got, want) {
				t.Errorf("Job args = %v, want %v", got, want)
			}
		}
	}
	if !bytes.Equal(data, originalData) || !slices.Equal(flags, originalFlags) {
		t.Error("concurrent invocation handling changed shared source or flags")
	}
}
