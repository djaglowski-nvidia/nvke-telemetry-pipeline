// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tests

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
)

var update = flag.Bool("update", false, "rewrite golden output files")

func TestPass(t *testing.T) {
	scenarios, err := os.ReadDir("pass")
	require.NoError(t, err)
	for _, scenario := range scenarios {
		t.Run(scenario.Name(), func(t *testing.T) {
			chart, err := loader.Load("test-chart")
			require.NoError(t, err)
			library, err := loader.Load("../chart")
			require.NoError(t, err)
			chart.SetDependencies(library)

			values, err := chartutil.ReadValuesFile(filepath.Join("pass", scenario.Name(), "values.yaml"))
			require.NoError(t, err)

			dryRun := action.NewInstall(new(action.Configuration))
			dryRun.DryRun = true
			dryRun.ClientOnly = true
			dryRun.ReleaseName = "test"
			dryRun.Namespace = "test"
			release, err := dryRun.Run(chart, values)
			require.NoError(t, err)

			goldenPath := filepath.Join("pass", scenario.Name(), "output.yaml")
			if *update {
				require.NoError(t, os.WriteFile(goldenPath, []byte(release.Manifest), 0o644))
				return
			}
			expected, err := os.ReadFile(goldenPath)
			require.NoError(t, err)
			assert.Equal(t, string(expected), release.Manifest,
				"rendered output does not match %s (run go test -update to regenerate)", goldenPath)
		})
	}
}

func TestFail(t *testing.T) {
	scenarios, err := os.ReadDir("fail")
	require.NoError(t, err)
	for _, scenario := range scenarios {
		t.Run(scenario.Name(), func(t *testing.T) {
			chart, err := loader.Load("test-chart")
			require.NoError(t, err)
			library, err := loader.Load("../chart")
			require.NoError(t, err)
			chart.SetDependencies(library)

			values, err := chartutil.ReadValuesFile(filepath.Join("fail", scenario.Name(), "values.yaml"))
			require.NoError(t, err)

			dryRun := action.NewInstall(new(action.Configuration))
			dryRun.DryRun = true
			dryRun.ClientOnly = true
			dryRun.ReleaseName = "test"
			dryRun.Namespace = "test"
			_, err = dryRun.Run(chart, values)
			require.Error(t, err, "expected configuration to be rejected")

			goldenPath := filepath.Join("fail", scenario.Name(), "error.txt")
			expected, readErr := os.ReadFile(goldenPath)
			require.NoError(t, readErr)

			assert.ErrorContains(t, err, strings.TrimSpace(string(expected)),
				"error does not contain expected message from %s", goldenPath)
		})
	}
}
