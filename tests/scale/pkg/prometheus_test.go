package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/format"
	"github.com/NVIDIA-dev/nktp/tests/scale/pkg/measurements"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

// TestExecutePrometheusPlan exercises argument binding, construction and reporting.
func TestExecutePrometheusPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm(), "ParseForm()")
		if got, want := r.Form.Get("query"), `count({test_run_id="integration-run"})`; got != want {
			t.Errorf("query = %q, want resolved run ID in %q", got, want)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[100,"3"]}]}}`)
	}))
	defer server.Close()
	data := []byte(fmt.Sprintf(`name: query-integration
timeout: 5s
stages:
  - name: metrics
    generator: {type: noop}
    measurement:
      type: prometheus
      spec:
        endpoint: %q
        duration: 200ms
        interval: 15ms
        units: {series: series}
        queries:
          series: 'count({test_run_id="{{ .Args.runId }}"})'
`, server.URL))
	args, err := ParseArguments([]string{"run-id=integration-run"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Parse(data, args)
	if err != nil {
		t.Fatal(err)
	}
	report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if report.RunID != "integration-run" || len(report.Stages) != 1 {
		t.Fatalf("report = %+v, want one stage with the supplied run ID", report)
	}
	stage := report.Stages[0]
	if stage.Name != "metrics" || stage.Measurement != "prometheus" {
		t.Errorf("stage attribution = %+v, want named Prometheus measurement", stage)
	}
	queries := stage.Report["queries"].(map[string]measurements.QueryReport)
	observations := queries["series"].Observations
	if len(observations) < 2 || observations[0].Value.(model.Vector)[0].Value != 3 {
		t.Errorf("observations = %+v, want continued polling after the no-op generator finishes", observations)
	}
	formatted := format.Format(stage.Report)
	for _, want := range []string{"series [series]", "Min: 3", "Mean: 3", "Max: 3", "Last: 3", "No data: 0", "Range ", "Observations"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted report lacks %q:\n%s", want, formatted)
		}
	}
	if strings.ContainsAny(formatted, "▁▂▃▄▅▆▇█") || strings.Contains(formatted, "Timeline:") || strings.Contains(formatted, "    Time ") {
		t.Errorf("formatted report contains a bar or timeline:\n%s", formatted)
	}
	if strings.Contains(queries["series"].Expression, "{{ .Args.") {
		t.Errorf("report retains an unresolved expression: %s", queries["series"].Expression)
	}
}
