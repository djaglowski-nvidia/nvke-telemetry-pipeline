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

The suite installs the chart itself, because installation is behaviour under
test. The Taskfile gives it only what a test cannot provide for itself: a Kind
cluster on the node image the root Taskfile pins, so that every suite in the
repository runs the same Kubernetes version, and the subcharts fetched from the
upstream Helm repositories. Individual scenarios document their setup,
assertions, and cleanup in their Go test comments.

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
created. In its default mode the suite installs and then removes releases, so it
holds the namespace to being empty at the end and expects the namespace to be
the release's.

To validate an NKTP installation without touching it, which is what qualifying
a release amounts to:

```sh
go test -tags e2e -count=1 -v -timeout 30m . -args \
  -nktp.install=false -nktp.kubeconfig=$KUBECONFIG -nktp.namespace=nktp
```

`-nktp.install=false` disables scenarios that own a release and validates the
existing installation in place. The suite only reads the cluster in that mode.

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

Reusable infrastructure lives in [../internal](../internal), not here: chart
installation and readiness in [../internal/framework](../internal/framework),
Kubernetes API helpers in [../internal/kube](../internal/kube), and the Helm
command wrapper in [../internal/helm](../internal/helm).
