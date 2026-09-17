package measurements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"go.yaml.in/yaml/v3"
)

// prometheusTestSpec builds a short collection window for HTTP tests.
func prometheusTestSpec(t *testing.T, endpoint string) yaml.Node {
	t.Helper()
	return measurementSpec(t, fmt.Sprintf("endpoint: %q\nduration: 200ms\ninterval: 15ms\nquery_timeout: 1s\nqueries:\n  throughput: 'sum(rate(points_total[5m]))'\n", endpoint))
}

// measurementSpec decodes test YAML into the component's spec node.
func measurementSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(data), &document); err != nil {
		t.Fatalf("decode test spec: %v", err)
	}
	return *document.Content[0]
}

// TestNewPrometheusRejectsInvalidSpecs checks validation before resource acquisition.
func TestNewPrometheusRejectsInvalidSpecs(t *testing.T) {
	const valid = "endpoint: http://127.0.0.1:1\nduration: 1s\nqueries: {load: up}\n"
	for _, test := range []struct {
		name string
		spec string
	}{
		{name: "sequence", spec: "[]"},
		{name: "missing endpoint", spec: "duration: 1s\nqueries: {load: up}"},
		{name: "missing duration", spec: "endpoint: http://localhost:9090\nqueries: {load: up}"},
		{name: "relative URL", spec: strings.Replace(valid, "http://127.0.0.1:1", "/prometheus", 1)},
		{name: "unsupported scheme", spec: strings.Replace(valid, "http://", "file://", 1)},
		{name: "credentials", spec: strings.Replace(valid, "http://", "http://user:password@", 1)},
		{name: "URL query", spec: strings.Replace(valid, ":1\n", ":1?tenant=test\n", 1)},
		{name: "URL fragment", spec: strings.Replace(valid, ":1\n", ":1#test\n", 1)},
		{name: "zero duration", spec: strings.Replace(valid, "1s", "0s", 1)},
		{name: "negative interval", spec: valid + "interval: -1s\n"},
		{name: "zero query timeout", spec: valid + "query_timeout: 0s\n"},
		{name: "numeric duration", spec: strings.Replace(valid, "1s", "100", 1)},
		{name: "null duration", spec: strings.Replace(valid, "1s", "null", 1)},
		{name: "unknown option", spec: valid + "warmup: 1s\n"},
		{name: "duplicate option", spec: valid + "duration: 2s\n"},
		{name: "empty queries", spec: strings.Replace(valid, "{load: up}", "{}", 1)},
		{name: "missing queries", spec: strings.Replace(valid, "queries: {load: up}\n", "", 1)},
		{name: "null queries", spec: strings.Replace(valid, "{load: up}", "null", 1)},
		{name: "numeric query", spec: strings.Replace(valid, "{load: up}", "{load: 10}", 1)},
		{name: "empty query", spec: strings.Replace(valid, "{load: up}", "{load: ' '}", 1)},
		{name: "empty name", spec: strings.Replace(valid, "{load: up}", "{'': up}", 1)},
		{name: "duplicate name", spec: strings.Replace(valid, "{load: up}", "{load: up, load: down}", 1)},
		{name: "alias", spec: strings.Replace(valid, "{load: up}", "{load: &query up, copy: *query}", 1)},
		{name: "unknown unit query", spec: valid + "units: {typo: bytes}\n"},
		{name: "numeric unit", spec: valid + "units: {load: 1}\n"},
		{name: "empty unit", spec: valid + "units: {load: ''}\n"},
		{name: "units sequence", spec: valid + "units: [bytes]\n"},
		{name: "duplicate unit", spec: valid + "units: {load: bytes, load: seconds}\n"},
		{name: "merge", spec: valid + "<<: {interval: 1s}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPrometheus(measurementSpec(t, test.spec))
			assert.Error(t, err, "NewPrometheus() succeeded, want invalid configuration error")
		})
	}
	measurement, err := NewPrometheus(measurementSpec(t, valid))
	if err != nil {
		t.Fatalf("NewPrometheus() contacted the unavailable backend or rejected valid settings: %v", err)
	}
	if measurement.config.Interval != 15*time.Second || measurement.config.QueryTimeout != 10*time.Second {
		t.Errorf("default timing = %+v, want interval 15s and query timeout 10s", measurement.config)
	}
}

// TestPrometheusRunRetainsObservations checks polling, labels, empty data and annotations.
func TestPrometheusRunRetainsObservations(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/api/v1/query" {
			t.Errorf("request = %s %s, want POST with the endpoint path prefix", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("query"); got != "sum(rate(points_total[5m]))" {
			t.Errorf("query = %q, want configured expression", got)
		}
		if _, err := strconv.ParseFloat(r.Form.Get("time"), 64); err != nil {
			t.Errorf("evaluation timestamp is invalid: %v", err)
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"instance":"first"},"value":[100.5,"3"]},{"metric":{"instance":"second"},"value":[100.5,"NaN"]}]},"warnings":["partial response"]}`)
	}))
	defer server.Close()
	measurement, err := NewPrometheus(prometheusTestSpec(t, server.URL+"/prefix/"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := measurement.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	observations := report["queries"].(map[string]QueryReport)["throughput"].Observations
	if len(observations) < 2 {
		t.Fatalf("Run() returned %d observations, want at least two", len(observations))
	}
	if len(observations[0].Value.(model.Vector)) != 0 || observations[0].Error != "" {
		t.Errorf("initial empty result = %+v, want explicit empty data without an error", observations[0])
	}
	got := observations[1]
	samples := got.Value.(model.Vector)
	if len(samples) != 2 || samples[0].Metric["instance"] != "first" || samples[1].Value.String() != "NaN" {
		t.Errorf("vector result = %+v, want both labeled samples with non-finite text retained", got)
	}
	if !reflect.DeepEqual(got.Warnings, []string{"partial response"}) {
		t.Errorf("backend annotations = %+v, want warnings retained", got)
	}
	if !report["finished_at"].(time.Time).After(report["started_at"].(time.Time)) {
		t.Error("report collection window did not advance")
	}
	if _, err := json.Marshal(report); err != nil {
		t.Errorf("report is not JSON serializable: %v", err)
	}
}

// TestPrometheusRunPreservesPartialReport checks that request failures retain earlier data.
func TestPrometheusRunPreservesPartialReport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			http.Error(w, `{"status":"error","errorType":"unavailable","error":"backend unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"scalar","result":[100,"12.5"]}}`)
	}))
	defer server.Close()
	measurement, err := NewPrometheus(prometheusTestSpec(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	measurement.config.Duration = time.Second
	report, err := measurement.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Run() error = %v, want HTTP failure", err)
	}
	var apiError *prometheusv1.Error
	if !errors.As(err, &apiError) || apiError.Type != prometheusv1.ErrServer {
		t.Errorf("Run() error = %v, want the official client's typed server error", err)
	}
	observations := report["queries"].(map[string]QueryReport)["throughput"].Observations
	if len(observations) != 2 || observations[0].Value.(*model.Scalar).Value != 12.5 || observations[1].Error == "" {
		t.Errorf("partial observations = %+v, want first sample and second request error", observations)
	}
}

// TestPrometheusRunCancellation checks caller cancellation and the normal collection cutoff.
func TestPrometheusRunCancellation(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller=%v", callerCancels), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm() error = %v", err)
				}
				if callerCancels {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			measurement, err := NewPrometheus(prometheusTestSpec(t, server.URL))
			if err != nil {
				t.Fatal(err)
			}
			report, err := measurement.Run(ctx)
			if callerCancels && !errors.Is(err, context.Canceled) || !callerCancels && err != nil {
				t.Errorf("Run() error = %v, want caller cancellation only when requested", err)
			}
			if report["finished_at"] == nil {
				t.Error("Run() omitted the partial report on cancellation")
			}
		})
	}
}

// TestPrometheusRunQueryTimeout bounds requests independently of the collection duration.
func TestPrometheusRunQueryTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	measurement, err := NewPrometheus(prometheusTestSpec(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	measurement.config.Duration = time.Second
	measurement.config.QueryTimeout = 20 * time.Millisecond
	report, err := measurement.Run(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want request timeout", err)
	}
	observations := report["queries"].(map[string]QueryReport)["throughput"].Observations
	if len(observations) != 1 || observations[0].Error == "" {
		t.Errorf("observations = %+v, want recorded request timeout", observations)
	}
}

// TestPrometheusRunCanceledBeforeStart avoids contacting a backend after cancellation.
func TestPrometheusRunCanceledBeforeStart(t *testing.T) {
	measurement, err := NewPrometheus(prometheusTestSpec(t, "http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := measurement.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
}

// TestPrometheusQueryRejectsBadResponses covers errors reported by the official client.
func TestPrometheusQueryRejectsBadResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: "not json"},
		{name: "string result", body: `{"status":"success","data":{"resultType":"string","result":[100,"text"]}}`},
		{name: "API error", body: `{"status":"error","errorType":"bad_data","error":"invalid query"}`},
		{name: "invalid timestamp", body: `{"status":"success","data":{"resultType":"scalar","result":["bad","1"]}}`},
		{name: "numeric value", body: `{"status":"success","data":{"resultType":"scalar","result":[100,1]}}`},
		{name: "invalid value", body: `{"status":"success","data":{"resultType":"scalar","result":[100,"bad"]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			measurement, err := NewPrometheus(prometheusTestSpec(t, server.URL))
			if err != nil {
				t.Fatal(err)
			}
			client, err := api.NewClient(api.Config{Address: measurement.config.Endpoint})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := queryPrometheus(t.Context(), prometheusv1.NewAPI(client), "up", time.Now(), time.Second); err == nil {
				t.Error("query() succeeded, want response validation error")
			}
		})
	}
}

// TestPrometheusQueryFallback retains parameters and the deadline across POST and GET.
func TestPrometheusQueryFallback(t *testing.T) {
	for _, expires := range []bool{false, true} {
		t.Run(fmt.Sprint(expires), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/prefix/api/v1/query" || r.Form.Get("query") != "up" || r.Form.Get("time") != "100.5" {
					t.Errorf("query request = %s %s %v", r.Method, r.URL, r.Form)
				}
				switch calls.Add(1) {
				case 1:
					if r.Method != http.MethodPost {
						t.Errorf("first method = %q, want POST", r.Method)
					}
					w.WriteHeader(http.StatusMethodNotAllowed)
				case 2:
					if r.Method != http.MethodGet {
						t.Errorf("fallback method = %q, want GET", r.Method)
					}
					if expires {
						<-r.Context().Done()
						return
					}
					fmt.Fprint(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"0"]}}`)
				default:
					t.Error("unexpected additional fallback request")
				}
			}))
			defer server.Close()
			client, err := api.NewClient(api.Config{Address: server.URL + "/prefix"})
			if err != nil {
				t.Fatal(err)
			}
			observation, err := queryPrometheus(t.Context(), prometheusv1.NewAPI(client), "up", time.Unix(100, 500000000), 100*time.Millisecond)
			if calls.Load() != 2 {
				t.Errorf("request count = %d, want one POST and one GET", calls.Load())
			}
			if expires {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("query error = %v, want deadline exceeded", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			scalar := observation.Value.(*model.Scalar)
			if observation.Value.Type() != model.ValScalar || scalar.Timestamp != 0 || scalar.Value != 0 {
				t.Errorf("fallback observation = %+v, want a valid zero sample", observation)
			}
		})
	}
}

// TestPrometheusModelFormatting verifies the official model's precision and values.
func TestPrometheusModelFormatting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"instance":"first"},"value":[100.1239,"1.00"]},{"metric":{},"value":[100.1239,"+Inf"]},{"metric":{},"value":[100.1239,"-Inf"]}]}}`)
	}))
	defer server.Close()
	client, err := api.NewClient(api.Config{Address: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := queryPrometheus(t.Context(), prometheusv1.NewAPI(client), "up", time.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	samples := observation.Value.(model.Vector)
	if len(samples) != 3 {
		t.Fatalf("sample count = %d, want three", len(samples))
	}
	for i, value := range []string{"1", "+Inf", "-Inf"} {
		sample := samples[i]
		if sample.Value.String() != value || sample.Timestamp != 100123 {
			t.Errorf("sample %d = %+v, want value %s at 100.123", i, sample, value)
		}
	}
}

// TestPrometheusQueryRetainsOfficialResults preserves types outside numeric summaries.
func TestPrometheusQueryRetainsOfficialResults(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "matrix", body: `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"instance":"a"},"values":[[100,"3"]]}]}}`, want: "matrix"},
		{name: "histogram", body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"instance":"a"},"histogram":[100,{"count":"2","sum":"3","buckets":[]}]}]}}`, want: "vector"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client, err := api.NewClient(api.Config{Address: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			observation, err := queryPrometheus(t.Context(), prometheusv1.NewAPI(client), "up", time.Now(), time.Second)
			if err != nil || observation.Value == nil || observation.Value.Type().String() != test.want {
				t.Fatalf("query = %+v, %v, want retained %s", observation, err, test.want)
			}
			data, err := json.Marshal(observation.Value)
			if err != nil {
				t.Fatalf("marshal raw result: %v", err)
			}
			var envelope struct {
				Data struct {
					Result json.RawMessage `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(test.body), &envelope); err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(envelope.Data.Result, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("raw result = %s, want %s", data, envelope.Data.Result)
			}
		})
	}
}
