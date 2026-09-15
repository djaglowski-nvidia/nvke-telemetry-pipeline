// Package runner executes scale test plans and consolidates measurement reports.
package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Execution controls scheduling between stages, not within a stage.
type Execution string

const (
	// Parallel runs stages concurrently and is the default execution mode.
	Parallel Execution = "parallel"
	// Sequential completes each stage before starting the next.
	Sequential Execution = "sequential"
)

// Plan describes a named test whose stages share an execution deadline.
// The empty Execution value means Parallel. Timeout must be positive.
type Plan struct {
	Name      string        `yaml:"name"`
	Execution Execution     `yaml:"execution"`
	Timeout   time.Duration `yaml:"timeout"`
	Stages    []Stage       `yaml:"stages"`
}

// Stage pairs one generator with one measurement, which run concurrently.
// Names must be nonempty and unique within the plan.
type Stage struct {
	Name        string        `yaml:"name"`
	Generator   ComponentSpec `yaml:"generator"`
	Measurement ComponentSpec `yaml:"measurement"`
}

// ComponentSpec selects an implementation and preserves its configuration.
// The selected implementation validates Spec before any stage starts.
type ComponentSpec struct {
	Type string    `yaml:"type"`
	Spec yaml.Node `yaml:"spec"`
}

// Parse decodes exactly one YAML document, rejects unknown envelope fields,
// defaults execution to Parallel, and validates the plan structure. It does not
// construct runnables; Executor.Execute validates implementation-specific specs.
func Parse(data []byte) (Plan, error) {
	var plan Plan
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode plan: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Plan{}, errors.New("plan must contain exactly one YAML document")
	}
	plan.setDefaults()
	if err := plan.validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// setDefaults supplies omitted execution settings without changing stage data.
func (p *Plan) setDefaults() {
	if p.Execution == "" {
		p.Execution = Parallel
	}
}

// validate checks the envelope for both parsed and programmatically built plans.
func (p Plan) validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("plan name is required")
	}
	if p.Execution != Parallel && p.Execution != Sequential {
		return fmt.Errorf("execution %q: want parallel or sequential", p.Execution)
	}
	if p.Timeout <= 0 {
		return errors.New("plan timeout must be positive")
	}
	if len(p.Stages) == 0 {
		return errors.New("plan must contain at least one stage")
	}
	names := make(map[string]bool, len(p.Stages))
	for i, stage := range p.Stages {
		if strings.TrimSpace(stage.Name) == "" {
			return fmt.Errorf("stages[%d].name is required", i)
		}
		if names[stage.Name] {
			return fmt.Errorf("duplicate stage name %q", stage.Name)
		}
		names[stage.Name] = true
		if stage.Generator.Type == "" || stage.Measurement.Type == "" {
			return fmt.Errorf("stage %q requires generator.type and measurement.type", stage.Name)
		}
	}
	return nil
}
