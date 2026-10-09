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
| `-keep-release` | Keep the existing deployed helm release | `false` |

**Example**:

```
task e2e:test -- -keep-release -chart /path/to/chart
```

The suite installs and removes its release; point it only at a disposable cluster.
