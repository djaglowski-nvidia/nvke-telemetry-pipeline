# End-to-End Tests

Tests of user-visible NKTP behavior after deploying the real chart to a Kind or explicitly provided Kubernetes cluster.

## Goals

- Validate installation, upgrades, removal, topology, dependencies, and Kubernetes API behavior.
- Verify complete telemetry paths where a scenario requires them.

## Constraints

- Must use isolated test resources and perform reliable cleanup.
- Must collect sufficient diagnostics to explain deployment and assertion failures.
- Scale and performance characterization belong in their dedicated suites.

## Running

Requires a Docker-compatible container runtime, [Kind](https://kind.sigs.k8s.io/),
[Task](https://taskfile.dev/installation/), Helm, kubectl, and Go.

```sh
task e2e:up      # create the cluster and fetch the chart dependencies
task e2e:test    # run the suite, as often as you like
task e2e:down    # delete the cluster
```

The cluster belongs to whoever brought it up, so the three are separate: CI
creates one, runs the suite, and deletes it, while a local run can keep one
across several attempts. `task e2e:down` is the only thing that deletes it, and
it deletes only what `task e2e:up` created: a cluster you brought yourself is
yours to remove, along with the kubeconfig that reaches it.

The suite installs the chart itself, because the install is the behaviour under
test. The Taskfile gives it only what a test cannot provide for itself: a Kind
cluster on the node image the root Taskfile pins, so that every suite in the
repository runs the same Kubernetes version, and the subcharts fetched from the
upstream Helm repositories.

`TestChartLifecycle` is the test, and its subtests are the loop:

1. install the chart, and wait for the workloads the operator builds after Helm
   returns
2. validate the release
3. reapply it, and check that nothing changed
4. validate it again
5. uninstall it, and check what is left behind

The loop is the unit the wider test matrix repeats, so a scenario is added by
varying its inputs rather than by adding a stage. Step 3 reapplies the current
release rather than upgrading from a published one, so it proves that
reinstalling changes nothing, not that an upgrade from an earlier version
works.

It refuses to start if the release name is already taken, since it would
otherwise adopt a release it did not create and delete it at the end.

"Nothing changed" is measured as the generation of every workload, since a name
only moves when a workload appears or disappears, while a template that
rendered differently would roll every pod under unchanged names. It is enforced
only for the workloads Helm applies itself, the ones carrying no owner
reference. Reapplying a Collector CR that Helm renders byte-identically still
makes the operator rewrite the workload derived from it, rolling every
collector pod, so that half is reported on each run rather than failing it.

A chart that generated something at install time, a hook writing unique
configuration for instance, would move the Helm-managed half for a reason of
its own. Nothing in the chart does today. `-nktp.stable-reapply=false` reports
that half rather than failing on it, for when something does.

Not covered yet:

- the configuration inside the Collector CRs, as distinct from their presence
- stale resources other than workloads, notably Services, ConfigMaps, and the
  PersistentVolumeClaims a StatefulSet leaves behind
- upgrading from a published release, which waits on the chart being published
- asserting that a custom values file is respected, rather than only accepting one
- the checks that a component is absent before an install and present after it,
  which need a suite of their own: asking the cluster whether an API is served
  cannot tell an NKTP component apart from one that was already there

## Pointing the suite somewhere else

`task e2e:test KUBECONFIG=<path>` runs against a cluster these tasks never
created. The lifecycle test installs and then removes a release, so it holds
the namespace to being empty at the end and expects the namespace to be the
release's.

To validate an NKTP installation without touching it, which is what qualifying
a release amounts to:

```sh
go test -tags e2e -count=1 -v -timeout 30m . -args \
  -nktp.install=false -nktp.kubeconfig=$KUBECONFIG -nktp.namespace=nktp
```

`-nktp.install=false` skips `TestChartLifecycle`, which owns a release from
install to uninstall, and runs `TestInstalled` instead. The suite only reads
the cluster in that mode.

Every input is a flag: `-nktp.chart`, `-nktp.release`, `-nktp.namespace`,
`-nktp.values` (repeatable, layered in order), `-nktp.timeout`, and
`-nktp.stable-reapply`. The suite refuses to run without a kubeconfig, so a
bare `go test` here cannot reach the cluster you are working on.

`task e2e:test VALUES=path/to/values.yaml` layers a values file on
top. `VALUES` is relative to the repository root and is applied after
[values.yaml](values.yaml), which sets Kind journal paths and cuts replica
counts and resource requests down to what a single node can schedule.

`task e2e:diagnose` reports the pods and warning events of a failed run. CI
runs it before deleting the cluster, since nobody can re-run a job and look at
one afterwards.

Reusable infrastructure lives in [../internal](../internal), not here: the
Kubernetes API helpers in [../internal/kube](../internal/kube) and the Helm
helper in [../internal/helm](../internal/helm).
