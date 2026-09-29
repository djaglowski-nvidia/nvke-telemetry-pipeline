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
[Task](https://taskfile.dev/installation/), Helm, kubectl, and Go. From the
repository root:

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

By default, each test installs a fresh Helm release before making its
assertions and removes that release afterward. The lifecycle test additionally
reapplies the release and verifies its removal. The Taskfile provides the Kind
cluster, cert-manager, and the chart dependencies needed before Go runs the
tests. Individual scenarios document their setup, assertions, and cleanup in
their Go test comments.

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

To reuse an NKTP release instead of reinstalling it, which is what qualifying
an existing release amounts to:

```sh
task e2e:up
task e2e:install
task e2e:test INSTALL=false
```

`task e2e:install` is an idempotent `helm upgrade --install` of the local chart.
It uses the E2E sizing values and the cluster created by `task e2e:up`. To point
both installation and testing at another cluster, pass the same kubeconfig to
both commands:

```sh
task e2e:install KUBECONFIG=<path>
task e2e:test KUBECONFIG=<path> INSTALL=false
```

The target cluster must already have cert-manager. Add one install-time values
file when needed:

```sh
task e2e:install KUBECONFIG=<path> HELM_VALUES_FILE=path/to/values.yaml
```

Absolute `HELM_VALUES_FILE` paths are used as-is. Relative paths are resolved
from `tests/e2e/`.

The shared install task applies the E2E sizing file first and
`HELM_VALUES_FILE` second. It uses `--reset-values`, so every invocation is
based on the chart defaults and those files rather than values retained by an
earlier Helm upgrade.

The equivalent direct Go invocation is:

```sh
(cd tests/e2e && go test -tags e2e -count=1 -v -timeout 30m . -args \
  -nktp.install=false -nktp.kubeconfig=.kubeconfig -nktp.namespace=nktp)
```

`-nktp.install` controls how every test obtains the NKTP installation it needs:

- `true` (the default) makes each test install a fresh release before running
  its assertions and remove it afterward.
- `false` makes every test require the release named by `-nktp.release` in
  `-nktp.namespace`. Each test fails if that exact Helm release does not exist,
  waits for its Operator-derived workloads to be ready, and then runs its
  assertions without installing, upgrading, or uninstalling the release. The
  lifecycle test verifies the existing installation but omits its reapply and
  removal stages.

Chart and values-file arguments affect only install mode; reuse mode tests the
configuration already stored in the cluster. Functional tests may still create
and clean up their own disposable fixtures, but they leave the Helm release
untouched.

Every input is a flag: `-nktp.chart`, `-nktp.release`, `-nktp.namespace`,
`-nktp.values` (repeatable, layered in order), `-nktp.timeout`,
`-nktp.install`, and `-nktp.stable-reapply`. The suite refuses to run without a
kubeconfig, so a bare `go test` here cannot reach the cluster you are working
on.

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
