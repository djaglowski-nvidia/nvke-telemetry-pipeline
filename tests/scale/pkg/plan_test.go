package runner

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
)

// planYAML describes two no-op stages and intentionally omits execution mode.
const planYAML = `name: smoke
timeout: 1m
stages:
  - name: first
    generator: {type: noop, spec: {}}
    measurement: {type: noop, spec: {}}
  - name: second
    generator: {type: noop, spec: {}}
    measurement: {type: noop, spec: {}}
`

// TestParseDefaults checks the default scheduling mode and preserved definitions.
func TestParseDefaults(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if plan.Execution != Parallel || plan.Timeout != time.Minute || len(plan.Stages) != 2 {
		t.Fatalf("Parse() = %+v, want parallel, 1m, two stages", plan)
	}
	if plan.Stages[0].Generator.Type != "noop" || plan.Stages[1].Name != "second" {
		t.Errorf("Parse().Stages = %+v, want the declared components and order", plan.Stages)
	}
}

// TestParseRejectsInvalidDocuments checks syntax, envelope fields, and topology.
func TestParseRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "decode plan"},
		{name: "syntax", data: "stages: [", want: "decode plan"},
		{name: "unknown field", data: planYAML + "typo: true\n", want: "field typo"},
		{name: "nested unknown field", data: strings.Replace(planYAML, "generator:", "generatr:", 1), want: "field generatr"},
		{name: "duplicate key", data: planYAML + "name: duplicate\n", want: "already defined"},
		{name: "second document", data: planYAML + "---\nname: extra\n", want: "exactly one"},
		{name: "empty trailing document", data: planYAML + "---\n", want: "exactly one"},
		{name: "missing name", data: strings.Replace(planYAML, "name: smoke", "name: ''", 1), want: "name is required"},
		{name: "bad mode", data: planYAML + "execution: unknown\n", want: "execution"},
		{name: "bad duration", data: strings.Replace(planYAML, "1m", "forever", 1), want: "decode plan"},
		{name: "zero timeout", data: strings.Replace(planYAML, "1m", "0s", 1), want: "timeout must be positive"},
		{name: "no stages", data: "name: empty\ntimeout: 1m\nstages: []\n", want: "at least one stage"},
		{name: "missing stage name", data: strings.Replace(planYAML, "name: first", "name: ''", 1), want: "stages[0].name"},
		{name: "duplicate stage name", data: strings.Replace(planYAML, "name: second", "name: first", 1), want: "duplicate stage"},
		{name: "missing generator", data: strings.Replace(planYAML, "generator: {type: noop, spec: {}}", "generator: {}", 1), want: "generator.type"},
		{name: "missing measurement", data: strings.Replace(planYAML, "measurement: {type: noop, spec: {}}", "measurement: {}", 1), want: "measurement.type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.data), nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse() error = %v, want an error containing %q", err, tc.want)
			}
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
		{name: "generator type", data: strings.ReplaceAll(planYAML, "generator: {type: noop", "generator: {type: missing"), want: `generator: unknown type "missing"`},
		{name: "measurement type", data: strings.ReplaceAll(planYAML, "measurement: {type: noop", "measurement: {type: missing"), want: `measurement: unknown type "missing"`},
		{name: "extra option", data: strings.Replace(planYAML, "spec: {}", "spec: {rate: 2}", 1), want: "noop spec must be an empty mapping"},
		{name: "scalar spec", data: strings.Replace(planYAML, "spec: {}", "spec: 3", 1), want: "noop spec must be an empty mapping"},
		{name: "null spec", data: strings.Replace(planYAML, "spec: {}", "spec: null", 1), want: "noop spec must be an empty mapping"},
		{name: "measurement extra option", data: strings.Replace(planYAML, "measurement: {type: noop, spec: {}}", "measurement: {type: noop, spec: {rate: 2}}", 1), want: "measurement: noop spec only supports a measurements sequence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse([]byte(tc.data), nil)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			report, err := (Executor{}).Execute(t.Context(), plan, "test-run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Execute() error = %v, want an error containing %q", err, tc.want)
			}
			if len(report.Stages) != 0 || report.RunID != "" {
				t.Errorf("Execute() report = %+v, want no execution for an invalid plan", report)
			}
		})
	}
}

// TestExecuteNoopPlan checks both public execution modes, report identity, and reuse.
func TestExecuteNoopPlan(t *testing.T) {
	for _, mode := range []Execution{"", Parallel, Sequential} {
		t.Run("mode="+string(mode), func(t *testing.T) {
			plan, err := Parse([]byte(planYAML), nil)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			plan.Execution = mode
			var previousID string
			for range 2 {
				args, err := ParseArguments(nil)
				if err != nil {
					t.Fatalf("ParseArguments() error = %v", err)
				}
				report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				if report.RunID != args[runIDArgument] || report.RunID == previousID || report.Plan != "smoke" {
					t.Errorf("Execute() identity = (%q, %q), previous ID = %q", report.Plan, report.RunID, previousID)
				}
				previousID = report.RunID
				want := []StageReport{
					{Name: "first", Measurement: "noop", Report: measurements.Report{}},
					{Name: "second", Measurement: "noop", Report: measurements.Report{}},
				}
				if !reflect.DeepEqual(report.Stages, want) {
					t.Errorf("Execute().Stages = %+v, want %+v", report.Stages, want)
				}
			}
			if plan.Execution != mode {
				t.Errorf("Execute() changed plan.Execution to %q, want %q", plan.Execution, mode)
			}
		})
	}
}

// TestExecuteRejectsInvalidInput checks the public API without relying on parsing.
func TestExecuteRejectsInvalidInput(t *testing.T) {
	if _, err := (Executor{}).Execute(t.Context(), Plan{}, "test-run"); err == nil {
		t.Error("Execute(Plan{}) error = nil, want validation error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Executor{}).Execute(ctx, Plan{}, "test-run"); err != context.Canceled {
		t.Errorf("Execute(canceled) error = %v, want context.Canceled", err)
	}
}

// TestExecuteUsesSuppliedRunID checks that reports retain the ID supplied during parsing.
func TestExecuteUsesSuppliedRunID(t *testing.T) {
	args, err := ParseArguments([]string{"run-id=ci-123"})
	if err != nil {
		t.Fatalf("ParseArguments() error = %v", err)
	}
	plan, err := Parse([]byte(planYAML), args)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for range 2 {
		report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if report.RunID != "ci-123" {
			t.Errorf("Execute().RunID = %q, want ci-123", report.RunID)
		}
	}
}

// TestExecuteRejectsInvalidRunID checks execution rejects missing or malformed IDs.
func TestExecuteRejectsInvalidRunID(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, id := range []string{"", "bad id"} {
		if _, err := (Executor{}).Execute(t.Context(), plan, id); err == nil || !strings.Contains(err.Error(), "run-id") {
			t.Errorf("Execute(runID=%q) error = %v, want run-id validation error", id, err)
		}
	}
}

// TestParseRejectsInvalidRunID checks direct callers cannot bind an invalid ID.
func TestParseRejectsInvalidRunID(t *testing.T) {
	for _, id := range []string{"", "bad id"} {
		t.Run("run-id="+id, func(t *testing.T) {
			if _, err := Parse([]byte(planYAML), map[string]string{runIDArgument: id}); err == nil || !strings.Contains(err.Error(), "run-id") {
				t.Errorf("Parse(run-id=%q) error = %v, want run-id validation error", id, err)
			}
		})
	}
}

// TestExecuteRejectsMismatchedRunID checks parsing binds a snapshot of the run ID.
func TestExecuteRejectsMismatchedRunID(t *testing.T) {
	args := map[string]string{runIDArgument: "run-a"}
	plan, err := Parse([]byte(planYAML), args)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	args[runIDArgument] = "run-b"
	report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("Execute() error = %v, want a run ID mismatch", err)
	}
	if !reflect.DeepEqual(report, PlanReport{}) {
		t.Errorf("Execute() report = %+v, want no execution on mismatch", report)
	}
	report, err = (Executor{}).Execute(t.Context(), plan, "run-a")
	if err != nil || report.RunID != "run-a" {
		t.Errorf("Execute() = %+v, %v, want success with the original run ID", report, err)
	}
}
