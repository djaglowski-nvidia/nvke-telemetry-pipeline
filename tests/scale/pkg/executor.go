package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
)

// generator owns a complete generation lifetime. Run is called once per instance,
// must honor cancellation, and must finish any owned goroutines and bounded
// cleanup before returning, including after partial startup. Nil means success.
type generator interface {
	Run(context.Context) error
}

// measurement observes and evaluates a stage concurrently with its generator.
// Run is called once per instance, must honor cancellation, and must finish its
// goroutines and bounded cleanup before returning. It may return a partial report
// with an error. A successful report may be empty. Failed checks return errors.
type measurement interface {
	Run(context.Context) (measurements.Report, error)
}

// StageReport attributes a measurement report and component errors to one stage.
// Errors remain available even when a measurement produces no report.
type StageReport struct {
	Name             string
	Measurement      string
	Report           measurements.Report
	GeneratorError   error
	MeasurementError error
}

// PlanReport consolidates one execution's results in stage declaration order.
type PlanReport struct {
	Plan   string
	RunID  string
	Stages []StageReport
}

// Executor validates and runs plans. Its zero value supports no-op plans.
// Execute calls are independent and may run concurrently. Callers supply the
// run ID resolved during argument parsing. Callers must not modify a plan or
// its specs while it is being executed.
type Executor struct {
	// Kubeconfig is the absolute path to the file used by Kubernetes-backed generators.
	// It is required only for generators that access a cluster. Credentials are read in Run.
	Kubeconfig string
}

// Execute constructs all runnables before starting any stage, then runs them
// under the plan timeout. Stage failures cancel their peer but not other stages.
// Cancellation stops unstarted stages and waits for started runnables to return.
// Reports, including partial findings, accompany execution errors. Invalid plans
// return an empty PlanReport. Completion uses the default slog logger.
// runID must match the ID retained by Parse when args contained runId.
// A mismatch returns an empty report before any component is constructed.
// No cluster is required by the no-op implementations.
func (e Executor) Execute(ctx context.Context, plan Plan, runID string) (PlanReport, error) {
	if err := ctx.Err(); err != nil {
		return PlanReport{}, err
	}
	if err := validateRunID(runID); err != nil {
		return PlanReport{}, err
	}
	if plan.runID != "" && plan.runID != runID {
		return PlanReport{}, fmt.Errorf("run-id %q does not match parsed plan run-id %q", runID, plan.runID)
	}
	plan.setDefaults()
	if err := plan.validate(); err != nil {
		return PlanReport{}, err
	}
	stages, err := prepareStages(plan.Stages, e.Kubeconfig)
	if err != nil {
		return PlanReport{}, err
	}
	logger := slog.With("plan", plan.Name, "run_id", runID)
	runCtx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()
	reports, err := executeStages(runCtx, logger, plan.Execution, stages)
	logger.InfoContext(ctx, "plan completed", "stages", len(reports), "error", err)
	return PlanReport{Plan: plan.Name, RunID: runID, Stages: reports}, err
}

// preparedStage binds validated specifications to fresh, inert runnables.
type preparedStage struct {
	name            string
	measurementType string
	generator       generator
	measurement     measurement
}

// prepareStages validates every implementation before execution acquires resources.
func prepareStages(stages []Stage, kubeconfig string) ([]preparedStage, error) {
	prepared := make([]preparedStage, 0, len(stages))
	for _, stage := range stages {
		g, err := newGenerator(stage.Generator, kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("stage %q generator: %w", stage.Name, err)
		}
		m, err := newMeasurement(stage.Measurement)
		if err != nil {
			return nil, fmt.Errorf("stage %q measurement: %w", stage.Name, err)
		}
		prepared = append(prepared, preparedStage{
			name: stage.Name, measurementType: stage.Measurement.Type,
			generator: g, measurement: m,
		})
	}
	return prepared, nil
}

// executeStages schedules independent stages and joins every started goroutine.
// Each stage owns one result slot. Aggregation starts only after all writers exit.
func executeStages(ctx context.Context, logger *slog.Logger, execution Execution, stages []preparedStage) ([]StageReport, error) {
	reports := make([]StageReport, len(stages))
	var wg sync.WaitGroup
	for i, stage := range stages {
		if execution == Parallel {
			wg.Go(func() { reports[i] = executeStage(ctx, logger, stage) })
		} else {
			reports[i] = executeStage(ctx, logger, stage)
		}
	}
	wg.Wait()
	var errs []error
	for _, report := range reports {
		if report.GeneratorError != nil {
			errs = append(errs, fmt.Errorf("stage %q generator: %w", report.Name, report.GeneratorError))
		}
		if report.MeasurementError != nil {
			errs = append(errs, fmt.Errorf("stage %q measurement: %w", report.Name, report.MeasurementError))
		}
	}
	return reports, errors.Join(append(errs, ctx.Err())...)
}

// executeStage runs both components to completion, canceling the peer on error.
// Successful completion does not stop the other component. Cancellation is checked
// before each Run and after it returns, so an ignored deadline cannot pass.
func executeStage(ctx context.Context, logger *slog.Logger, stage preparedStage) StageReport {
	report := StageReport{Name: stage.name, Measurement: stage.measurementType}
	stageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stageCtx.Err() == nil {
		logger.InfoContext(stageCtx, "stage started", "stage", stage.name)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		err := stageCtx.Err()
		if err == nil {
			err = stage.generator.Run(stageCtx)
		}
		report.GeneratorError = errors.Join(err, stageCtx.Err())
		if report.GeneratorError != nil {
			cancel()
		}
	})
	wg.Go(func() {
		err := stageCtx.Err()
		if err == nil {
			report.Report, err = stage.measurement.Run(stageCtx)
		}
		report.MeasurementError = errors.Join(err, stageCtx.Err())
		if report.MeasurementError != nil {
			cancel()
		}
	})
	wg.Wait()
	logger.InfoContext(ctx, "stage completed", "stage", stage.name,
		"generator_error", report.GeneratorError, "measurement_error", report.MeasurementError)
	return report
}
