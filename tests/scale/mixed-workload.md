# Mixed workload

- [Plan](plans/mixed-workload.yaml) · [Profile](mixed-workload-values.yaml)
- Load: 100k series and about 4 GiB/hour each of logs and spans per worker
- Paths: node, Events and large-target collectors → gateways → blackhole
- NKTP: chart resource and autoscaling defaults, with mTLS for collector-to-gateway and observation traffic

## Load

The plan runs nine generators in parallel within one stage, alongside a shared
Prometheus measurement. Init containers stagger each group to approximately
25%, 50% and 100% load at zero, five and ten minutes. Delays start when each Pod
is scheduled, so waves can overlap. Small groups round the split.

Each of 100 workers runs 100 local generator Pods:

| Group | Pods/node | Metrics/Pod | Logs/Pod | Spans/Pod |
| --- | ---: | --- | --- | --- |
| Small, scraped | 50 | 200 scraped series | 100 B × 16/s | — |
| Small, instrumented | 40 | 250 series over OTLP | 100 B × 16/s | 1 KiB × 16/s |
| Node-local targets | 4 | 20,000 scraped series | — | — |
| Large logs | 4 | — | 64 KiB × 4/s | — |
| Large traces | 2 | — | — | 64 KiB × 4/s |

| Signal | Per node | Across 100 workers |
| --- | ---: | ---: |
| Synthetic series | 90k scraped + 10k OTLP | 10M |
| Log records | 1,456/s | 145,600/s |
| Log bodies | 1,192,576 B/s | 119,257,600 B/s |
| Spans | 648/s | 64,800/s |
| Span payloads | 1,179,648 B/s | 117,964,800 B/s |

- Metrics: Avalanche, four generated labels, no churn, 30s scrapes / 10s timeout. Instrumented Pods use private Avalanche targets and an OTLP adapter.
- Logs: Vector `demo_logs` → stdout → runtime files → native filelog receiver.
- Adjust load: Pod counts and Avalanche arguments in the plan, log/span sizes and rates in the profile ConfigMaps. Rate calculations are beside each value. Span rates count spans, with two spans per trace.
- Bytes: log bodies and span payload attributes. Newline, CRI and OTLP overhead are additional. Platform telemetry adds background load.

The large-target collector pool handles these additional targets at **60s / 55s**:

| Series/target | Pods | Total series |
| ---: | ---: | ---: |
| 150,000 | 24 | 3,600,000 |
| 300,000 | 24 | 7,200,000 |
| 600,000 | 6 | 3,600,000 |
| Total | 54 | 14,400,000 |

One k6 Pod per worker creates 100 Events with 1 KiB messages, then
updates them at 10/s for about 90 minutes: **1,000 updates/s** across 10,000 objects.
See [Kubernetes Events](kube-events.md) for why k6 is used and how Events are cleaned up.
The combined workload has 24.4M synthetic series.

## Cluster

| Pool | Nodes | Node size | Required label |
| --- | ---: | --- | --- |
| Generators | 100 | 16 vCPU / 32 GiB | `nodeGroup=customer-cpu` |
| NKTP and support | 8 | 64 vCPU / 128 GiB | `nodeGroup=system-cpu` |

- Setup: [shared prerequisites](README.md#setup), existing fleet, healthy Metrics Server for HPA. Workers must reach LGTM on port 9090 for k6 metrics.
- Placement: 101 synthetic Pods on 46 workers and 102 on 54 workers, plus platform services. Reserve Pod slots and addresses.
- Blackhole and bundled LGTM: 72 CPU / 80 GiB requested, plus capacity for NKTP and HPA.
- Profile: adapt placement and API access settings to your cluster. Keep an untainted node for Helm cleanup. Set `CERT_MANAGER_VALUES_FILE` if needed.

## Run

Stop other generators. From the repository root, install the adapted profile
alone, including its `extraObjects`. Installation waits for readiness and forwards
[Prometheus](http://127.0.0.1:9090) and [Grafana](http://127.0.0.1:3000).
Bundled NKTP dashboards are available in Grafana. When using a custom internal
CA, update the profile's client Certificate issuer to match.

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
task scale:install KUBECONFIG="$KUBECONFIG" INSTALL_KWOK=false \
  HELM_VALUES_FILE=tests/scale/mixed-workload-values.yaml
RUN_ID="mixed-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=125m -- \
  -plan "$PWD/tests/scale/plans/mixed-workload.yaml" -arg run-id="$RUN_ID"
```

## Results

- Window: 30 minutes at full load after the ramp. Verify per-node placement and throughput before selecting the window.
- Runtime: 90 minutes per StatefulSet after readiness and per Event producer after seeding, 110-minute observations, 115-minute plan ceiling. Cancel after saving the selected window.
- Load: targets, series, stdout records, received logs/spans, payload and serialized OTLP bytes and singleton Event acceptance.
- Performance: generator, main collector and sidecar CPU/memory, observed peaks, throttling, queues and HPA replicas.
- Health: readiness, restarts, termination reasons, refusals, export failures and observation freshness.
- Events: require 1,000 run-specific singleton receipts/s across 100 workers, 10 successful PATCHes/s per producer, and zero failed PATCHes or dropped iterations. Exclude seeding and cleanup.
- Cleanup: automatic generator removal on completion, failure or cancellation. Retain LGTM and the dashboard for review.

## Limitations

- This plan records observations without automated performance checks. Missing or stale data cannot qualify the run.
- Check 64 KiB log reconstruction and rotation. Source scrape health alone does not establish OTLP delivery.
- Repetitive payloads compress unusually well. Blackhole cannot verify durable delivery. LGTM storage is temporary.
