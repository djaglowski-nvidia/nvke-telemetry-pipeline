//go:build scale

package generators

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestAvalancheCommand runs the plan's shell wrapper with finite test workloads.
// It shortens the timer while preserving the command and exit handling.
func TestAvalancheCommand(t *testing.T) {
	data, err := os.ReadFile("../../plans/avalanche.yaml")
	require.NoError(t, err, "read Avalanche plan")
	var plan struct {
		Stages []struct {
			Generator struct {
				Spec jobConfig `yaml:"spec"`
			} `yaml:"generator"`
		} `yaml:"stages"`
	}
	require.NoError(t, yaml.Unmarshal(data, &plan), "decode Avalanche plan")
	require.Len(t, plan.Stages, 1, "one Avalanche workload")
	spec := plan.Stages[0].Generator.Spec
	require.Equal(t, []string{"/bin/sh", "-c"}, spec.Cmd, "shell wrapper")
	require.GreaterOrEqual(t, len(spec.Args), 3, "wrapper and workload arguments")
	require.Contains(t, spec.Args[0], "seq 180", "three-minute timer")
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{name: "planned stop", body: "exec sleep 30", code: 0},
		{name: "early failure", body: "exit 7", code: 7},
		{name: "early success", body: "exit 0", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			workload := filepath.Join(t.TempDir(), "test workload")
			require.NoError(t, os.WriteFile(workload, []byte("#!/bin/sh\n"+test.body+"\n"), 0700), "write test workload")
			args := append([]string(nil), spec.Args...)
			args[0] = strings.Replace(args[0], "seq 180", "seq 1", 1)
			args[2] = workload
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, spec.Cmd[0], append(spec.Cmd[1:], args...)...)
			command.WaitDelay = time.Second
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError, "run wrapper: %s", output)
				code = exitError.ExitCode()
			}
			assert.Equal(t, test.code, code, "wrapper exit: %s", output)
		})
	}
}
