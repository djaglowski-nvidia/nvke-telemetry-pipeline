package runner

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/generators"
	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
)

// generatorFunc implements a generator with test-controlled behavior.
type generatorFunc func(context.Context) error

// Run executes the configured generation behavior.
func (f generatorFunc) Run(ctx context.Context) error { return f(ctx) }

// measurementFunc implements a measurement with test-controlled behavior.
type measurementFunc func(context.Context) (measurements.Report, error)

// Run executes the configured measurement behavior.
func (f measurementFunc) Run(ctx context.Context) (measurements.Report, error) { return f(ctx) }

// TestStageComponentsOverlap requires each runnable to observe the other's start.
func TestStageComponentsOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		generatorStarted := make(chan struct{})
		measurementStarted := make(chan struct{})
		stage := preparedStage{
			name: "overlap", measurementType: "test",
			generator: generatorFunc(func(context.Context) error {
				close(generatorStarted)
				<-measurementStarted
				return nil
			}),
			measurement: measurementFunc(func(context.Context) (measurements.Report, error) {
				close(measurementStarted)
				<-generatorStarted
				return measurements.Report{"observed": true}, nil
			}),
		}
		report := executeStage(t.Context(), slog.Default(), stage)
		if report.GeneratorError != nil || report.MeasurementError != nil || report.Report["observed"] != true {
			t.Errorf("executeStage() = %+v, want both runnables to finish successfully", report)
		}
	})
}

// TestStageScheduling checks overlap, successful peer completion, and report order.
func TestStageScheduling(t *testing.T) {
	for _, mode := range []Execution{Parallel, Sequential} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				releaseFirst := make(chan struct{})
				started := [2]bool{}
				stages := make([]preparedStage, 2)
				for i, name := range []string{"first", "second"} {
					stages[i] = preparedStage{
						name: name, measurementType: "test",
						generator: generatorFunc(func(context.Context) error {
							started[i] = true
							if i == 0 {
								<-releaseFirst
							}
							return nil
						}),
						measurement: measurementFunc(func(context.Context) (measurements.Report, error) {
							return measurements.Report{"name": name}, nil
						}),
					}
				}
				var reports []StageReport
				var err error
				done := make(chan struct{})
				go func() {
					reports, err = executeStages(t.Context(), slog.Default(), mode, stages)
					close(done)
				}()
				synctest.Wait()
				if want := [2]bool{true, mode == Parallel}; started != want {
					t.Errorf("started = %v, want %v", started, want)
				}
				select {
				case <-done:
					t.Error("executeStages() returned before the first generator finished")
				default:
				}
				close(releaseFirst)
				<-done
				if err != nil {
					t.Fatalf("executeStages() error = %v", err)
				}
				want := []StageReport{
					{Name: "first", Measurement: "test", Report: measurements.Report{"name": "first"}},
					{Name: "second", Measurement: "test", Report: measurements.Report{"name": "second"}},
				}
				if !reflect.DeepEqual(reports, want) {
					t.Errorf("executeStages() reports = %+v, want %+v", reports, want)
				}
			})
		})
	}
}

// TestStageFailureIsIsolated checks both failure directions and partial reports.
func TestStageFailureIsIsolated(t *testing.T) {
	boom := errors.New("component failed")
	for _, tc := range []struct {
		name           string
		generatorError error
		measureError   error
	}{
		{name: "generator", generatorError: boom},
		{name: "measurement", measureError: boom},
	} {
		for _, mode := range []Execution{Parallel, Sequential} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					generatorStarted := make(chan struct{})
					measurementStarted := make(chan struct{})
					peerCleaned := false
					// cleanup models a resource owner finishing after cancellation.
					cleanup := func(ctx context.Context) error {
						<-ctx.Done()
						cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
						defer cancel()
						time.Sleep(10 * time.Millisecond)
						peerCleaned = cleanupCtx.Err() == nil
						return ctx.Err()
					}
					stages := []preparedStage{
						{
							name: "failing", measurementType: "test",
							generator: generatorFunc(func(ctx context.Context) error {
								close(generatorStarted)
								<-measurementStarted
								if tc.generatorError != nil {
									return tc.generatorError
								}
								return cleanup(ctx)
							}),
							measurement: measurementFunc(func(ctx context.Context) (measurements.Report, error) {
								close(measurementStarted)
								<-generatorStarted
								report := measurements.Report{"samples": 2}
								if tc.measureError != nil {
									return report, tc.measureError
								}
								return report, cleanup(ctx)
							}),
						},
						{name: "healthy", measurementType: "noop", generator: generators.Noop{}, measurement: measurements.Noop{}},
					}
					reports, err := executeStages(t.Context(), slog.Default(), mode, stages)
					if !errors.Is(err, boom) || !errors.Is(err, context.Canceled) {
						t.Errorf("executeStages() error = %v, want component failure and canceled peer", err)
					}
					if !peerCleaned {
						t.Error("executeStages() returned before peer cleanup completed")
					}
					if reports[0].Report["samples"] != 2 {
						t.Errorf("partial report = %v, want samples=2", reports[0].Report)
					}
					if reports[1].GeneratorError != nil || reports[1].MeasurementError != nil || reports[1].Report == nil {
						t.Errorf("healthy stage = %+v, want successful execution", reports[1])
					}
				})
			})
		}
	}
}

// TestPlanCancellation checks active peers stop and sequential stages stay unstarted.
func TestPlanCancellation(t *testing.T) {
	for _, mode := range []Execution{Parallel, Sequential} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := [2][2]bool{}
				stages := make([]preparedStage, 2)
				for i, name := range []string{"first", "second"} {
					stages[i] = preparedStage{
						name: name, measurementType: "test",
						generator: generatorFunc(func(ctx context.Context) error {
							started[i][0] = true
							<-ctx.Done()
							return ctx.Err()
						}),
						measurement: measurementFunc(func(ctx context.Context) (measurements.Report, error) {
							started[i][1] = true
							<-ctx.Done()
							return measurements.Report{"partial": true}, ctx.Err()
						}),
					}
				}
				var reports []StageReport
				var err error
				done := make(chan struct{})
				go func() {
					reports, err = executeStages(ctx, slog.Default(), mode, stages)
					close(done)
				}()
				synctest.Wait()
				cancel()
				<-done
				if !errors.Is(err, context.Canceled) {
					t.Errorf("executeStages() error = %v, want context.Canceled", err)
				}
				wantStarted := [2][2]bool{{true, true}, {mode == Parallel, mode == Parallel}}
				if started != wantStarted {
					t.Errorf("started = %v, want %v", started, wantStarted)
				}
				for i, report := range reports {
					if !errors.Is(report.GeneratorError, context.Canceled) || !errors.Is(report.MeasurementError, context.Canceled) {
						t.Errorf("reports[%d] = %+v, want cancellation attributed to both components", i, report)
					}
					if started[i][1] && report.Report["partial"] != true {
						t.Errorf("reports[%d].Report = %v, want partial findings", i, report.Report)
					}
				}
			})
		})
	}
}

// TestIgnoredDeadlineCannotPass checks a runnable returning nil after cancellation.
func TestIgnoredDeadlineCannotPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		stage := preparedStage{
			name: "late", measurementType: "noop",
			generator: generatorFunc(func(context.Context) error {
				time.Sleep(2 * time.Second)
				return nil
			}),
			measurement: measurements.Noop{},
		}
		_, err := executeStages(ctx, slog.Default(), Parallel, []preparedStage{stage})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("executeStages() error = %v, want context.DeadlineExceeded", err)
		}
	})
}
