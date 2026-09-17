package measurements

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"go.yaml.in/yaml/v3"
)

// Prometheus collects named PromQL observations without performance verdicts.
// Construct it with NewPrometheus. Each Run owns its report and collection window.
type Prometheus struct {
	// config holds validated collection and connection settings.
	config prometheusConfig
}

// prometheusConfig defines a collection window independent of generator lifetime.
type prometheusConfig struct {
	Endpoint     string            `yaml:"endpoint"`
	Duration     time.Duration     `yaml:"duration"`
	Interval     time.Duration     `yaml:"interval"`
	QueryTimeout time.Duration     `yaml:"query_timeout"`
	Queries      map[string]string `yaml:"queries"`
	Units        map[string]string `yaml:"units"`
}

// QueryReport retains the expression and completed polling attempts in order.
type QueryReport struct {
	Expression   string             `json:"expression"`
	Unit         string             `json:"unit,omitempty"`
	Observations []QueryObservation `json:"observations"`
}

// QueryObservation retains one official Prometheus result, including empty data.
// Value keeps labels, timestamps and non-finite values in the library's format.
// Backend warnings may indicate incomplete results.
type QueryObservation struct {
	EvaluatedAt time.Time   `json:"evaluated_at"`
	Value       model.Value `json:"value"`
	Warnings    []string    `json:"warnings,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// NewPrometheus validates configuration without contacting the backend.
// Endpoint is an HTTP or HTTPS base URL reachable from the test process.
// Duration and a nonempty queries mapping are required. Interval defaults to
// 15 seconds and query_timeout to 10 seconds. Durations must be positive.
// Queries are checked for nonempty text. PromQL syntax is validated by the backend.
func NewPrometheus(spec yaml.Node) (*Prometheus, error) {
	if err := validatePrometheusSpec(spec); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(&spec)
	if err != nil {
		return nil, fmt.Errorf("encode prometheus spec: %w", err)
	}
	cfg := prometheusConfig{Interval: 15 * time.Second, QueryTimeout: 10 * time.Second}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode prometheus spec: %w", err)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("endpoint must be an absolute HTTP or HTTPS base URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("endpoint must not contain credentials, query parameters or a fragment")
	}
	if cfg.Duration <= 0 || cfg.Interval <= 0 || cfg.QueryTimeout <= 0 {
		return nil, errors.New("duration, interval and query_timeout must be positive")
	}
	if len(cfg.Queries) == 0 {
		return nil, errors.New("queries must contain at least one named PromQL expression")
	}
	for name := range cfg.Units {
		if _, ok := cfg.Queries[name]; !ok {
			return nil, fmt.Errorf("unit refers to unknown query %q", name)
		}
	}
	return &Prometheus{config: cfg}, nil
}

// validatePrometheusSpec rejects aliases and scalar coercion before typed decoding.
// Typed decoding additionally rejects duplicate names and unknown configuration.
func validatePrometheusSpec(spec yaml.Node) error {
	if spec.Kind != yaml.MappingNode {
		return errors.New("prometheus spec must be a mapping")
	}
	for i := 0; i < len(spec.Content); i += 2 {
		key, value := spec.Content[i], spec.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return errors.New("prometheus option names must be strings")
		}
		if key.Value == "queries" || key.Value == "units" {
			if value.Kind != yaml.MappingNode {
				return fmt.Errorf("%s must be a mapping of names to strings", key.Value)
			}
			for _, entry := range value.Content {
				if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" || strings.TrimSpace(entry.Value) == "" {
					return fmt.Errorf("%s names and values must be nonempty strings", key.Value)
				}
			}
		} else if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", key.Value)
		}
	}
	return nil
}

// Run queries immediately, then polls at the configured interval until duration
// elapses. Queries within a round run sequentially with one evaluation timestamp.
// Slow rounds may skip ticks. A request still running at the end of the window
// is canceled and omitted. Completed observations are retained on every exit.
// Empty results and non-finite values are findings, not performance failures.
// Request errors or caller cancellation return a partial report and an error.
// Successful completion does not stop the stage's generator.
func (p *Prometheus) Run(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, p.config.Duration)
	defer cancel()
	queries := make(map[string]QueryReport, len(p.config.Queries))
	for name, expression := range p.config.Queries {
		queries[name] = QueryReport{Expression: expression, Unit: p.config.Units[name], Observations: []QueryObservation{}}
	}
	report := Report{"started_at": started, "interval": p.config.Interval.String(), "queries": queries}
	defer func() { report["finished_at"] = time.Now().UTC() }()
	client, err := api.NewClient(api.Config{Address: p.config.Endpoint})
	if err != nil {
		return report, fmt.Errorf("create Prometheus client: %w", err)
	}
	queryAPI := prometheusv1.NewAPI(client)
	names := slices.Sorted(maps.Keys(queries))
	ticker := time.NewTicker(p.config.Interval)
	defer ticker.Stop()
	for {
		at := time.Now().UTC()
		for _, name := range names {
			if runCtx.Err() != nil {
				return report, ctx.Err()
			}
			query := queries[name]
			observation, err := queryPrometheus(runCtx, queryAPI, query.Expression, at, p.config.QueryTimeout)
			if errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil && ctx.Err() == nil {
				return report, nil
			}
			if err != nil {
				observation.Error = err.Error()
			}
			query.Observations = append(query.Observations, observation)
			queries[name] = query
			if err != nil {
				return report, fmt.Errorf("query %q: %w", name, err)
			}
		}
		select {
		case <-runCtx.Done():
			return report, ctx.Err()
		case <-ticker.C:
		}
	}
}

// queryPrometheus retains the official result with a deadline for the entire query.
func queryPrometheus(ctx context.Context, client prometheusv1.API, expression string, at time.Time, timeout time.Duration) (QueryObservation, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, warnings, err := client.Query(queryCtx, expression, at)
	return QueryObservation{EvaluatedAt: at, Value: value, Warnings: warnings}, err
}
