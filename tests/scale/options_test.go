//go:build scale

package scale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveKubeconfig checks single-file selection and override precedence.
func TestResolveKubeconfig(t *testing.T) {
	pathList := "/tmp/first" + string(os.PathListSeparator) + "/tmp/second"
	for _, test := range []struct {
		name      string
		path      string
		args      []string
		want      string
		wantError string
	}{
		{name: "dev default", want: "../../dev/.kubeconfig"},
		{name: "explicit path", path: "/tmp/custom kubeconfig", want: "/tmp/custom kubeconfig"},
		{name: "relative path", path: "fixtures/kubeconfig", want: "fixtures/kubeconfig"},
		{name: "trailing assignment overrides default", path: "/tmp/default", args: []string{"KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "multiple files", path: pathList, wantError: "lists multiple files: pass one path"},
		{name: "multiple files in trailing assignment", path: "/tmp/default", args: []string{"KUBECONFIG=" + pathList}, wantError: "lists multiple files: pass one path"},
		{name: "empty list entry", path: "/tmp/first" + string(os.PathListSeparator), wantError: "lists multiple files: pass one path"},
		{name: "single trailing assignment overrides file list", path: pathList, args: []string{"KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "last trailing assignment wins", args: []string{"KUBECONFIG=" + pathList, "KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "unknown positional argument", args: []string{"KUBECONFG=/tmp/typo"}, wantError: "unexpected argument"},
		{name: "empty assignment", args: []string{"KUBECONFIG="}, wantError: "unexpected argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveKubeconfig(test.path, test.args)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Errorf("resolveKubeconfig() = %q, error = %v, want error containing %q", got, err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveKubeconfig() error = %v", err)
			}
			want, err := filepath.Abs(test.want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("resolveKubeconfig() = %q, want %q", got, want)
			}
		})
	}
}
