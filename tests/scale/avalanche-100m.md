# Avalanche 100M

Reproduce the completed experiment's **100M active-series workload** with 15,658
stock Avalanche Pods. Six generator stages run in parallel with one shared
observation stage. Each Pod exposes one target with stable counters and no churn.

| Series per target | Pods | Total series | Scraper |
| ---: | ---: | ---: | --- |
| 1,000 | 7,960 | 7.96M | Node-local |
| 10,000 | 7,604 | 76.04M | Node-local |
| 40,000 | 40 | 1.60M | Node-local |
| 150,000 | 24 | 3.60M | Large-target pool |
| 300,000 | 24 | 7.20M | Large-target pool |
| 600,000 | 6 | 3.60M | Large-target pool |
| **Total** | **15,658** | **100M** | |

Scrapes run every 60 seconds, offering **1.667M generated points/s**. Node collectors
handle 85.6M series and the shared pool handles 14.4M. Both paths feed the metrics
gateway and four blackhole collectors. Selected performance observations reach
LGTM. Watchdog keeps its default destination.

The [profile](avalanche-100m-values.yaml) preserves NKTP resource, replica,
autoscaling, runtime and queue defaults. It adds test monitors, blackhole and
required EKS placement and API access. It reproduces the workload, with the
55-second large-target scrape timeout established during the experiment, but
does not include the later NKTP resource tuning. Node-local timeout is 10 seconds.

## Cluster

Use the completed experiment's fleet as the capacity reference:

| Pool | Nodes | Node size | Required label |
| --- | ---: | --- | --- |
| Generators | 100 | `c8i-flex.4xlarge`, 16 vCPU / 32 GiB | `nodeGroup=customer-cpu` |
| NKTP and support | 8 | `c8a.16xlarge`, 64 vCPU / 128 GiB | `nodeGroup=system-cpu` |

The recorded allocatable capacity was about 15.9 CPU / 26.2 GiB per worker and
63.8 CPU / 112.3 GiB per system node. The profile places workloads but does not
provision or resize nodes. Verify current capacity before running.

Spreading averages 156.6 generators per worker and can place up to 161 on one
worker. Provide at least 161 free Pod slots per worker in addition to cluster
agents. The tested workers advertised 394 slots. A 110-Pod limit will not fit.
Budget subnet addresses for 15,658 producer Pods, other workloads, replacements
and CNI warm allocations. The prior planning allowance was roughly 32k addresses.

Producers request about 164 CPU and 517 GiB in total. Their combined limits are
about 795 CPU and 1,034 GiB. Each blackhole requests 8 CPU / 8 GiB and has limits
of 16 CPU / 16 GiB. Reserve capacity for HPA growth and system workloads. This
fleet is a measured reference, not a guarantee that every HPA maximum or all
container limits fit simultaneously.

Default LGTM and Helm cleanup require an untainted schedulable node. Make one
system node available without a scheduling taint, or provide a separate utility
node with spare capacity. Reserve roughly 16 CPU / 32 GiB for LGTM on that node.
The profile retains the chart's LGTM and cleanup resources without replacements.

Workers may have `skyhook.nvidia.com=runtime-required:NoSchedule` and system nodes
may have `dedicated=system-workload:NoSchedule`. The profile expects existing
cert-manager with its CRDs, metrics-server and API Service address `172.20.0.1`.
For another cluster, adapt labels, taints and API access. Keep operator network defaults enabled and verify
cross-pool scraping, OTLP, DNS and kubelet connectivity.

## Run

From the repository root, authenticate and select the EKS kubeconfig. Requires
kubectl, Helm, Task and Go, with cert-manager and metrics-server already installed.
Replace the kubeconfig path below with your cluster’s kubeconfig.

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"

helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts --force-update
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update
helm dependency build nktp

task install KUBECONFIG_FILE="$KUBECONFIG" HELM_TIMEOUT=20m \
  HELM_VALUES_FILE=tests/scale/avalanche-100m-values.yaml
```

Supply this profile alone on every installation. It replaces previous release
values. KWOK is unnecessary for real Pods. Wait for all collectors, allocators,
blackhole and LGTM Pods to become Ready before starting the test.

In a second terminal with the same `KUBECONFIG`, keep this forward running:

```sh
kubectl --kubeconfig "$KUBECONFIG" -n nktp port-forward \
  service/lgtm 9090:9090 3000:3000
```

Back in the original terminal, run one experiment at a time:

```sh
RUN_ID="avalanche100m-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=130m -- \
  -plan "$PWD/tests/scale/plans/avalanche-100m.yaml" \
  -arg run-id="$RUN_ID"
```

Each producer runs for 75 minutes from its own start. Observations run for 100
minutes, with a 110-minute plan deadline. Require all producers Ready within 20
minutes, allow 15 minutes to settle, then assess 30 uninterrupted minutes before
the earliest producer stops. Stop the run if that window cannot be achieved.

## Evaluate

Compare each bucket with the workload table throughout the acceptance window:

- Fresh successful targets and each diagnostic target count must match the Pod
  count. Both minimum and maximum samples must match the series per target.
- At the end of 30 settled minutes, successful target counts must still match.
  Each target needs at least 30 scrape and sample observations. The historical
  minimum and maximum samples must both match the expected target size.
- Producer inventory, readiness and fresh CPU and memory counts must match the
  Pod count for each Avalanche container. Check CPU and memory for every reported
  NKTP Pod and fresh node coverage against the cluster inventory.

All six buckets must appear. Missing results are unavailable data, not zero or
success. Per-target sample extremes identify endpoints to inspect when a bucket
range is wrong. Inspect queues, failures, restarts and HPA state. Compare gateway output
with blackhole acceptance, accounting for background traffic. Evaluate history
queries at the end of the chosen interval before producers stop. Whole-run
summaries include startup and cleanup.

## Limitations and cleanup

The completed experiment failed at default NKTP budgets. Node collectors ran out
of memory and the default 8 MiB node queue rejected complete 40k-series scrapes.
The tuned run also had observation losses. This profile deliberately preserves
NKTP defaults, so it does not claim a passing 100M result.

Queries record evidence without enforcing performance thresholds. Blackhole
cannot prove unique-series persistence or complete delivery. Missing or delayed
observations invalidate coverage claims. Missing failure counters are not zero.

Parallel stages fail independently. Stop the whole run if a stage fails.
The current framework can report an error when completed Pods disappear. Its
five-minute automatic cleanup budget can also leave Pods at this scale. After
stopping the harness, remove any remaining producers from that run:

```sh
kubectl --kubeconfig "$KUBECONFIG" -n nktp delete pod \
  -l "scale.nktp.io/workload=avalanche-100m,scale.nktp.io/run-id=$RUN_ID" \
  --wait=true --timeout=15m
```

Verify removal and wait for queues and memory to settle before repeating. Save
results before uninstalling NKTP while preserving the cluster:

```sh
helm uninstall nktp --kubeconfig "$KUBECONFIG" -n nktp --wait --timeout=20m
```

Stop the port-forward with Ctrl+C in its terminal.
