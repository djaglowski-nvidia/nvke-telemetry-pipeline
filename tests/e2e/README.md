# End-to-End Tests

Tests of user-visible NKTP behavior after deploying the real chart to a Kind or explicitly provided Kubernetes cluster.

## Goals

- Validate installation, upgrades, removal, topology, dependencies, and Kubernetes API behavior.
- Verify complete telemetry paths where a scenario requires them.

## Running

Requires a Docker-compatible container runtime, [Kind](https://kind.sigs.k8s.io/),
[Task](https://taskfile.dev/installation/), Helm, kubectl, and Go. From the
repository root:

```sh
task e2e:up      # create the cluster and fetch the chart dependencies
task e2e:test    # run the suite, as often as you like
task e2e:down    # delete the cluster
```

## Flags

`task e2e:test` passes these suite flags:

| Flag | Purpose | Default |
| --- | --- | --- |
| `-kubeconfig` | Cluster kubeconfig; specify explicitly to avoid using another context | `tests/e2e/.kubeconfig` via Task |
| `-chart` | Chart directory | `nktp/` |
| `-namespace` | Release namespace | `nktp` |
| `-values` | E2E values file | `tests/e2e/values.yaml` |
| `-keep-release` | Reuse a deployed release if present and keep the release after testing | `false` |

To test a different chart, edit the absolute chart path below:

```sh
E2E_CHART=/path/to/chart
task e2e:test -- -chart "$E2E_CHART"
```

With `-keep-release`, an existing deployed release is reused and `-chart` is
ignored. If no deployed release exists, the suite installs the specified chart
and keeps it after testing.

By default, the suite replaces its existing release and removes it after testing;
point it only at a disposable cluster.
