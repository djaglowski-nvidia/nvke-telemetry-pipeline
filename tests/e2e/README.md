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

Override the cluster with `task e2e:test KUBECONFIG=/path/to/kubeconfig`.
For other overrides, run Go directly from `tests/e2e/` and pass suite flags after `-args`:

```sh
go test -tags e2e -count=1 -v -timeout 30m . -args \
  -kubeconfig=.kubeconfig -chart=../../nktp -namespace=nktp -values=values.yaml
```

The suite installs and removes its release; point it only at a disposable cluster.
