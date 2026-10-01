# Watchdog load

The [plan](plans/watchdog-5000.yaml) creates five StatefulSets concurrently in one
stage, with 1,000 Pods each. Each set runs for 30 minutes after all its Pods first
become Ready.
Each Pod sends 100 metric points and 100 log records per minute with 100 KiB log
bodies, offering **8,333 points/s and 8,333 logs/s**, or **814 MiB/s of log bodies**.

The [profile](watchdog-values.yaml) uses the chart's watchdog resource and
exporter defaults. Synthetic logs pass through watchdog to four overprovisioned
blackhole collectors. The profile also supplies an overprovisioned LGTM backend
for metrics and performance observations.

Use workers labeled `nodeGroup=customer-cpu` for generators and nodes labeled
`nodeGroup=system-cpu` for blackhole and LGTM. Adapt the labels and tolerations to
your cluster. Generators require 5,000 pod slots and addresses, 1,000 requested
CPUs and 1,250 GiB of requested memory.

Blackhole and LGTM together request 112 CPU and 128 GiB of memory. LGTM needs
48 CPU and 96 GiB available on one system node. Allow additional capacity for
NKTP and its autoscaling. A healthy Metrics Server is required. KWOK is not needed.

## Run

Stop other generators. Combine [watchdog-values.yaml](watchdog-values.yaml) with
your cluster configuration, keeping unrelated `extraObjects`. Replace existing
custom LGTM objects with the profile's versions. If cert-manager also needs node
selectors or tolerations, provide its Helm values separately through
`CERT_MANAGER_VALUES_FILE`.

From the repository root, select your kubeconfig and install the combined values:

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
task scale:install INSTALL_KWOK=false \
  HELM_VALUES_FILE=/path/to/watchdog-cluster-values.yaml
```

The installer waits for NKTP, blackhole and LGTM to be Ready and forwards
Prometheus on port 9090 and Grafana on port 3000. Run the test:

```sh
RUN_ID="watchdog-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=55m -- \
  -plan "$PWD/tests/scale/plans/watchdog-5000.yaml" \
  -arg run-id="$RUN_ID"
```

Require all 5,000 Pods with both containers Ready within ten minutes. Once all
sets are Ready, allow five minutes to settle and assess 15 minutes before the
earliest set finishes. Each set has its own timer, so use their overlapping load
interval. Compare throughput, CPU, memory, queues, replicas, failures and data
freshness. Container restarts do not fail the test by themselves. Check
`producer_restarts`, `fresh_sources` and throughput for load interruptions.
Telemetrygen runs until the framework removes its StatefulSet.

## Cleanup and limitations

The harness removes each StatefulSet, its Pods and its headless Service, allowing
five minutes for cleanup. If cleanup fails, delete only the StatefulSets named in
the error with foreground propagation. Wait for their Pods to disappear before
starting another run. Deleting only Pods causes replacements.

- This approximates telemetry volume and client count, not the full collector
  telemetry mix.
- Repetitive log bodies compress unusually well, reducing network load.
- Queries do not enforce performance thresholds. Missing observations are not
  zero, and blackhole does not verify storage or durable delivery.
- LGTM storage is temporary. Save observations before replacing the environment.
