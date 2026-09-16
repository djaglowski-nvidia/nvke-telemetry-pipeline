package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// Examples must parse and construct without contacting a cluster or reading credentials.
func TestExamplePlans(t *testing.T) {
	paths, err := filepath.Glob("../plans/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("find example plans: paths=%v error=%v", paths, err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if _, err := prepareStages(plan.Stages, "/example/kubeconfig"); err != nil {
				t.Errorf("prepareStages() error = %v", err)
			}
		})
	}
}
