package runner

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseArguments preserves supplied values and leaves the input unchanged.
func TestParseArguments(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   map[string]string
	}{
		{
			name:   "supplied run ID",
			values: []string{"run-id=ci-123", "sample-rate=100", "gateway=http://example.test?a=b"},
			want: map[string]string{
				"runId": "ci-123", "sampleRate": "100", "gateway": "http://example.test?a=b",
			},
		},
		{
			name:   "literal values",
			values: []string{"run-id=ci.123_test", "empty=", "text= {{ .Args.other }}\n: ${ENV}"},
			want: map[string]string{
				"runId": "ci.123_test", "empty": "", "text": " {{ .Args.other }}\n: ${ENV}",
			},
		},
		{
			name:   "maximum run ID length",
			values: []string{"run-id=" + strings.Repeat("a", 63)},
			want:   map[string]string{"runId": strings.Repeat("a", 63)},
		},
		{
			name:   "single character ID",
			values: []string{"run-id=1"},
			want:   map[string]string{"runId": "1"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string(nil), test.values...)
			got, err := ParseArguments(test.values)
			if err != nil {
				t.Fatalf("ParseArguments() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("ParseArguments() = %v, want %v", got, test.want)
			}
			if !reflect.DeepEqual(test.values, before) {
				t.Errorf("ParseArguments() changed input to %v, want %v", test.values, before)
			}
		})
	}
}

// TestParseArgumentsGeneratesFreshRunID checks that each invocation gets its own default ID.
func TestParseArgumentsGeneratesFreshRunID(t *testing.T) {
	raw := []string{"rate=100"}
	seen := make(map[string]bool)
	for range 3 {
		args, err := ParseArguments(raw)
		if err != nil {
			t.Fatalf("ParseArguments() error = %v", err)
		}
		id := args[runIDArgument]
		if !runIDPattern.MatchString(id) || seen[id] {
			t.Errorf("ParseArguments() run-id = %q, want a fresh valid ID", id)
		}
		seen[id] = true
		if args["rate"] != "100" {
			t.Errorf("ParseArguments() rate = %q, want 100", args["rate"])
		}
		args["rate"] = "changed in result"
	}
	if want := []string{"rate=100"}; !reflect.DeepEqual(raw, want) {
		t.Errorf("ParseArguments() changed input to %v, want %v", raw, want)
	}
}

// TestParseArgumentsRejectsInvalidFlags checks argument names, duplicates, and run IDs.
func TestParseArgumentsRejectsInvalidFlags(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   string
	}{
		{name: "missing equals", values: []string{"rate"}, want: "NAME=VALUE"},
		{name: "empty name", values: []string{"=100"}, want: "valid name"},
		{name: "leading digit", values: []string{"1rate=100"}, want: "valid name"},
		{name: "dot in name", values: []string{"rate.value=100"}, want: "valid name"},
		{name: "trailing hyphen", values: []string{"sample-rate-=100"}, want: "valid name"},
		{name: "repeated hyphen", values: []string{"sample--rate=100"}, want: "valid name"},
		{name: "whitespace in name", values: []string{" rate=100"}, want: "valid name"},
		{name: "duplicate argument", values: []string{"rate=10", "rate=20"}, want: "duplicate argument"},
		{name: "duplicate normalized argument", values: []string{"sample-rate=10", "sampleRate=20"}, want: "duplicate argument"},
		{name: "duplicate run ID", values: []string{"run-id=first", "runId=second"}, want: "duplicate argument"},
		{name: "empty run ID", values: []string{"run-id="}, want: "run-id must"},
		{name: "oversize run ID", values: []string{"run-id=" + strings.Repeat("a", 64)}, want: "run-id must"},
		{name: "leading punctuation", values: []string{"run-id=-test"}, want: "run-id must"},
		{name: "trailing punctuation", values: []string{"run-id=test."}, want: "run-id must"},
		{name: "quote in run ID", values: []string{"run-id=test\"id"}, want: "run-id must"},
		{name: "newline in run ID", values: []string{"run-id=test\nid"}, want: "run-id must"},
		{name: "unicode run ID", values: []string{"run-id=tést"}, want: "run-id must"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseArguments(test.values); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("ParseArguments() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}
