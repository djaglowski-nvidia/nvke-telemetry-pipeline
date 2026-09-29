// Package helm manages chart releases on a test cluster.
//
// The helpers here drive the helm binary rather than linking its Go SDK, so a
// test installs a chart with the same commands the documentation gives users.
// Fetching a chart's dependencies is not here: that needs network access to
// the upstream repositories, which is a prerequisite of a run rather than
// something under test.
package helm

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Release identifies one chart installation.
type Release struct {
	Name      string
	Namespace string
}

// Client runs helm against the cluster one kubeconfig describes.
type Client struct {
	// Kubeconfig is the file helm reads its cluster from. It is passed on every
	// invocation rather than exported to the environment, so a test cannot
	// reach a cluster through an inherited KUBECONFIG.
	Kubeconfig string

	// Log receives helm's own output, which is the only account of what an
	// install did when one fails.
	Log io.Writer
}

// Install installs or upgrades a release using chart defaults and the supplied
// values files in order, discarding previous release overrides. It waits for
// resources Helm manages, excluding workloads created by other controllers.
func (c *Client) Install(ctx context.Context, r Release, chart string, valueFiles []string, timeout time.Duration) error {
	args := []string{
		"upgrade", "--install", r.Name, chart, "--reset-values",
		"--namespace", r.Namespace, "--create-namespace",
		"--wait", "--timeout", timeout.String(),
	}
	for _, file := range valueFiles {
		args = append(args, "--values", file)
	}
	return c.run(ctx, args...)
}

// Uninstall removes a release and waits for the resources helm applied itself
// to go away. It succeeds when the release is not installed.
func (c *Client) Uninstall(ctx context.Context, r Release, timeout time.Duration) error {
	return c.run(ctx,
		"uninstall", r.Name,
		"--namespace", r.Namespace,
		"--ignore-not-found", "--wait", "--timeout", timeout.String())
}

// Installed reports whether a release exists, in any state.
func (c *Client) Installed(ctx context.Context, r Release) (bool, error) {
	cmd := exec.CommandContext(ctx, "helm",
		"status", r.Name, "--kubeconfig", c.Kubeconfig, "--namespace", r.Namespace)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	// helm reports a missing release as an error, which is an answer rather
	// than a failure. Anything else is a failure to ask the question.
	if strings.Contains(string(output), "release: not found") {
		return false, nil
	}
	return false, fmt.Errorf("helm status %s: %w: %s", r.Name, err, output)
}

func (c *Client) run(ctx context.Context, args ...string) error {
	args = append(args, "--kubeconfig", c.Kubeconfig)
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stdout, cmd.Stderr = c.Log, c.Log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helm %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
