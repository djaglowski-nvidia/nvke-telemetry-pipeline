# 10,000 Pod churn

The [plan](plans/10k-pods.yaml) starts 100 Jobs together with 100 concurrent Pods each.
Pods sleep for 600 seconds before replacement, offering about 16.7 replacements/s
at full occupancy. Replacements can arrive in bursts. Measurements cover the first
35 minutes. Allow about 40 minutes plus startup for each Job to finish 400 Pods.
The plan times out after 45 minutes.

The [profile](10k-pods-values.yaml) uses default NKTP resources and autoscaling.
KSM metrics pass through collectors and the metrics gateway to four blackhole
replicas. Blackhole forwards Pod state, node capacity, NKTP resource budgets,
restarts and autoscaling metrics to LGTM, alongside non-KSM metrics such as
cAdvisor. Other KSM metrics and test logs are discarded. Watchdog sends directly
to LGTM.

## Cluster

Reference fleet used for the experiment:

| Pool | Nodes | Node size | Required label |
| --- | ---: | --- | --- |
| Generators | 100 | `c8i-flex.4xlarge`, 16 vCPU / 32 GiB | `nodeGroup=customer-cpu` |
| NKTP and support | 8 | `c8a.16xlarge`, 64 vCPU / 128 GiB | `nodeGroup=system-cpu` |

Allow 100 free Pod slots per worker plus headroom for uneven placement,
replacements and cluster agents. Budget Pod addresses and CNI warm allocations.
The profile does not provision nodes. Adapt placement and the API Service address
in the profile to your cluster.

Sleepers request 100 CPU / 312.5 GiB in total. Each blackhole requests 4 CPU / 4 GiB
with 16 CPU / 16 GiB limits. One LGTM instance requests 8 CPU / 16 GiB with
16 CPU / 32 GiB limits. Reserve capacity for NKTP and HPA growth.
Keep an untainted schedulable node available for the upstream Helm uninstall hook.

## Run

Stop other generators and run from the repository root. Supply this profile alone
so earlier release values are not carried over. Installation includes cert-manager,
waits for readiness and forwards Prometheus to port 9090 and Grafana to port 3000.
KWOK is not needed for these real Pods.
A healthy Metrics Server is required for HPA. Use `CERT_MANAGER_VALUES_FILE` if
cert-manager needs placement settings.

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
task scale:install KUBECONFIG="$KUBECONFIG" INSTALL_KWOK=false \
  HELM_VALUES_FILE=tests/scale/10k-pods-values.yaml
RUN_ID="pods-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=46m -- \
  -plan "$PWD/tests/scale/plans/10k-pods.yaml" -arg run-id="$RUN_ID"
```

Compare fresh Pod counts with the Kubernetes API. Assess component CPU, memory,
KSM scrape health, throughput, queues and failures during churn.

## Cleanup and limitations

The framework attempts to remove each Job and its Pods after completion, failure
or cancellation, allowing 30 seconds for cleanup. If cleanup reports an error,
verify the remaining workload Pods are gone before starting another run.

- A failed Pod fails the run without retries.
- 10,000 is a target. Startup, replacement, controller throughput, scheduling and
  Pod IP allocation can reduce achieved concurrency.
- Completed Pods can accumulate until their Job is removed. Churn also grows LGTM series.
  Stop if retained Pods or backend memory exceed available capacity. LGTM storage
  is temporary.
- Queries do not enforce thresholds. Missing data is not zero, scrapes can miss
  short-lived states and blackhole cannot prove complete delivery.
