package measurements

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestNoopAcceptsQueriesWithoutEvaluatingThem checks query examples produce no findings.
func TestNoopAcceptsQueriesWithoutEvaluatingThem(t *testing.T) {
	for _, spec := range []string{
		"",
		"{}",
		"queries: {}",
		"queries: {example: 'deliberately invalid query syntax'}",
	} {
		t.Run(spec, func(t *testing.T) {
			var node yaml.Node
			if spec != "" {
				var document yaml.Node
				if err := yaml.Unmarshal([]byte(spec), &document); err != nil {
					t.Fatalf("Unmarshal() error = %v", err)
				}
				node = *document.Content[0]
			}
			measurement, err := NewNoop(node)
			if err != nil {
				t.Fatalf("NewNoop() error = %v", err)
			}
			report, err := measurement.Run(t.Context())
			if err != nil || len(report) != 0 {
				t.Errorf("Run() = %v, %v, want an empty successful report", report, err)
			}
		})
	}
}

// TestNoopRejectsInvalidSpecs checks the shape and string values of query mappings.
func TestNoopRejectsInvalidSpecs(t *testing.T) {
	for _, spec := range []string{
		"null",
		"[]",
		"rate: 1",
		"queries: null",
		"queries: []",
		"queries: {}\nextra: true",
		"queries: {}\nqueries: {}",
		"queries: {'': query}",
		"queries: {1: query}",
		"queries: {example: ''}",
		"queries: {example: ' '}",
		"queries: {example: 100}",
		"queries: {example: true}",
		"queries: {example: null}",
		"queries: {example: []}",
		"queries: {example: first, example: second}",
		"queries: {first: &query text, second: *query}",
		"queries: {<<: {example: query}}",
	} {
		t.Run(spec, func(t *testing.T) {
			var document yaml.Node
			if err := yaml.Unmarshal([]byte(spec), &document); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if _, err := NewNoop(*document.Content[0]); err == nil {
				t.Error("NewNoop() error = nil, want an invalid spec error")
			}
		})
	}
}
