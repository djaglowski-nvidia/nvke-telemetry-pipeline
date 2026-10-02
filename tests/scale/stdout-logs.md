# Stdout log generation

- [Plan](plans/stdout-logs.yaml) · [Profile](stdout-logs-values.yaml)
- Design: logs-only [PR #108](https://github.com/NVIDIA-dev/nktp/pull/108) load, 4× baseline rates
- Path: Vector stdout → runtime files → node collectors → logs gateways → blackhole
- NKTP: default resources and autoscaling, LGTM observations

## Load

| Bucket | Pods/node | Body size | Records/s/Pod | Records/s/node | Body bytes/s/node |
| --- | ---: | ---: | ---: | ---: | ---: |
| Small | 90 | 100 B | 16 | 1,440 | 144,000 |
| Large | 4 | 64 KiB | 4 | 16 | 1,048,576 |
| Total | 94 | | | 1,456 | 1,192,576 |

- Cluster total: 9,400 Pods, 145,600 records/s, 119,257,600 body bytes/s
- Body throughput: excludes newline, CRI and OTLP overhead

## Cluster

| Pool | Nodes | Node size | Required label |
| --- | ---: | --- | --- |
| Generators | 100 | `c8i-flex.4xlarge`, 16 vCPU / 32 GiB | `nodeGroup=customer-cpu` |
| NKTP and support | 8 | `c8a.16xlarge`, 64 vCPU / 128 GiB | `nodeGroup=system-cpu` |

- Setup: [shared prerequisites](README.md#setup), existing fleet, healthy Metrics Server for HPA
- Per worker: 94 free Pod slots and addresses, 2.35 CPU / 5.875 GiB requested, plus platform headroom
- Blackhole and LGTM: 24 CPU / 32 GiB requested, additional capacity for NKTP and HPA
- Profile: adapt labels, tolerations and API Service address to your cluster
- Helm cleanup hook: one untainted schedulable node
- Cert-manager: set `CERT_MANAGER_VALUES_FILE` if system-node placement is required

## Run

- Working directory: repository root, other generators stopped
- Installation: profile alone, cert-manager, readiness checks, Prometheus on port 9090 and Grafana on port 3000

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
task scale:install KUBECONFIG="$KUBECONFIG" INSTALL_KWOK=false \
  HELM_VALUES_FILE=tests/scale/stdout-logs-values.yaml
RUN_ID="stdout-logs-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=115m -- \
  -plan "$PWD/tests/scale/plans/stdout-logs.yaml" -arg run-id="$RUN_ID"
```

## Results

- Execution: small, large and observation stages run concurrently
- Runtime: 90 minutes per StatefulSet after all replicas become Ready, 100-minute observations from stage start, 105-minute plan ceiling
- Throughput: generated records/s and body bytes/s per node/bucket
- Performance: average and peak CPU, memory, throttling, queue occupancy and gateway replicas
- Health: readiness, restarts, OOMs, refusals and export failures
- Performance thresholds: not enforced by the harness
- Cleanup: automatic on completion, failure or cancellation
- Removed: StatefulSets, Pods and headless Services, five-minute budget per set
- LGTM: keeps results in temporary storage

## Limitations

- Compression: repetitive bodies compress unusually well
- Delivery: rotation can lose unread logs, blackhole cannot verify durable delivery
- Missing data is not zero
