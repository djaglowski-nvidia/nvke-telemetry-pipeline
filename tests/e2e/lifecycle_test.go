//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

// TestChartLifecycle installs the chart, validates it, reapplies it, validates
// it again, removes it, and checks what the removal left behind. That loop is
// the unit the wider test matrix repeats, so a new scenario is a different set
// of inputs to it rather than another stage.
//
// It owns the release from end to end, which is why the steps are subtests of
// one test rather than tests in their own right: nothing here is meaningful
// out of order, and Go orders subtests but not tests.
func TestChartLifecycle(t *testing.T) {
	if !*install {
		t.Skip("-nktp.install is unset, so this suite does not own a release to install or remove")
	}

	k := newKube(t)
	rel := releaseRef()

	installed := t.Run("the chart installs", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		// This test removes the release at the end, and Install upgrades one
		// that already exists rather than refusing it. Between them they would
		// take over a release the suite did not create and then delete it, so
		// the name has to be free before any of that starts.
		existing, err := newHelm(t).Installed(ctx, rel)
		require.NoError(t, err)
		require.False(t, existing,
			"release %s already exists in namespace %s, and this test would take it over and then remove it; validate it where it stands with -nktp.install=false",
			rel.Name, rel.Namespace)

		require.NoError(t, newHelm(t).Install(ctx, rel, *chart, valueFiles, *timeout), "installing the chart")

		waitCtx, cancelWait := deadline(t)
		defer cancelWait()
		require.NoError(t, settle(waitCtx, k), "waiting for the release to come up")
	})
	// A release that failed to install says nothing about the chart's
	// behaviour once installed, and an uninstall check run against one would
	// report on whatever the failure happened to leave.
	require.True(t, installed, "the rest of the lifecycle needs an installed release")

	t.Run("an installed release", func(t *testing.T) { verifyInstalled(t, k) })

	t.Run("reapplying the release changes nothing", func(t *testing.T) {
		before, err := k.Workloads(t.Context(), *namespace)
		require.NoError(t, err)

		ctx, cancel := deadline(t)
		defer cancel()
		require.NoError(t, newHelm(t).Install(ctx, rel, *chart, valueFiles, *timeout), "reapplying the chart")

		waitCtx, cancelWait := deadline(t)
		defer cancelWait()
		require.NoError(t, settle(waitCtx, k), "waiting for the reapplied release")

		after, err := k.Workloads(t.Context(), *namespace)
		require.NoError(t, err)

		// Helm applies these itself and sets no owner reference on them, so
		// nothing between the chart and the cluster can touch them: if one
		// moved, the chart rendered differently on the second pass.
		//
		// A chart that generates something at install time, a hook that
		// writes unique configuration for instance, would move them for a
		// reason of its own. Nothing in the chart does today, so this holds;
		// -nktp.stable-reapply=false reports it instead once something does.
		wasHelm, isHelm := generations(before, false), generations(after, false)
		if *stableReapply {
			assert.Equal(t, wasHelm, isHelm,
				"reapplying the same release rolled or changed a workload Helm manages")
		} else if !slices.Equal(wasHelm, isHelm) {
			t.Log("note: reapplying the release moved a workload Helm manages")
		}

		// Reported rather than enforced. Reapplying a Collector CR that Helm
		// renders byte-identically still makes the operator rewrite the
		// workload it derives from it, rolling every collector pod. That is
		// the operator's behaviour to fix, not something the chart can be held
		// to meanwhile.
		if !slices.Equal(generations(before, true), generations(after, true)) {
			t.Log("note: the operator rewrote the workloads it derives from unchanged Collector CRs, so a reapply rolled every collector pod")
		}
	})

	t.Run("an installed release, reapplied", func(t *testing.T) { verifyInstalled(t, k) })

	t.Run("uninstalling takes the workloads with it", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()
		require.NoError(t, newHelm(t).Uninstall(ctx, rel, *timeout), "uninstalling the release")

		// Kubernetes' garbage collector removes the workloads the operator
		// derives, once the Collector they reference is gone. `helm uninstall
		// --wait` waits only for the objects Helm applied itself, so it
		// returns while that is still in progress.
		//
		// This holds the namespace to being empty rather than to holding none
		// of the release's own objects, so it assumes the namespace is the
		// release's.
		waitCtx, cancelWait := deadline(t)
		defer cancelWait()
		assert.NoError(t, k.WaitWorkloadsGone(waitCtx, *namespace), "uninstall should leave no workloads behind")
	})

	t.Run("uninstalling leaves the CRDs in place", func(t *testing.T) {
		// The CRDs come from the crds/ directories of the bundled charts,
		// which Helm never deletes, so an NKTP uninstall leaves a cluster's
		// other Collectors and monitors alone.
		for _, crd := range []struct{ group, resource string }{
			{"opentelemetry.io", "opentelemetrycollectors"},
			{"monitoring.coreos.com", "servicemonitors"},
		} {
			served, err := k.ServesResource(crd.group, crd.resource)
			require.NoError(t, err)
			assert.True(t, served, "%s.%s should survive an uninstall", crd.resource, crd.group)
		}
	})
}

// settle waits for the workloads the operator builds once Helm has applied the
// Collector CRs it builds them from.
//
// `helm --wait` covers only the objects Helm applies itself, so it returns
// before those exist. Waiting for whatever is in the namespace at that moment
// would pass by looking at a namespace the operator has not finished filling.
func settle(ctx context.Context, k *kube.Client) error {
	crs, err := k.CustomResources(ctx, *namespace, collectors)
	if err != nil {
		return err
	}
	expected, err := collectorWorkloads(crs)
	if err != nil {
		return err
	}
	if err := k.WaitWorkloadsExist(ctx, *namespace, expected); err != nil {
		return err
	}
	return k.WaitWorkloadsReady(ctx, *namespace)
}

// generations renders the workloads one side owns, so that comparing two
// rounds reports which of them moved rather than only that something did.
//
// Generation is what separates a reapply from a rollout: names alone would
// catch a workload appearing or disappearing, while a template that renders
// differently on each pass rolls every pod under unchanged names.
func generations(workloads []kube.Workload, derived bool) []string {
	var out []string
	for _, w := range workloads {
		if w.Derived == derived {
			out = append(out, fmt.Sprintf("%s generation %d", w, w.Generation))
		}
	}
	return out
}
