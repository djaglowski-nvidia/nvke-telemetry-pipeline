# Kubernetes Event load

The [plan](plans/kube-burner-events.yaml) runs 100 kube-burner Pods, one per worker,
offering **10,000 Events/s**. Each Pod creates 12,000 distinct Events with 1 KiB
messages at 100 Events/s. The run offers 1.2 million Events over roughly two
minutes and collects performance measurements for five minutes.

The [profile](kube-burner-events-values.yaml) uses NKTP's default resource settings
with two cluster-events replicas, one active and one standby. Events flow through
the logs gateway to four overprovisioned blackhole collectors. LGTM retains
performance metrics and watchdog telemetry.

Requires 100 available workers labeled `nodeGroup=customer-cpu`, one per generator.
Nodes labeled `nodeGroup=system-cpu` host central NKTP components, blackhole and
LGTM, separating them from the generators. Node-local collectors run across both
groups. Adapt these labels, tolerations and API access rules to your cluster.

Blackhole and LGTM together request 24 CPU and 32 GiB of memory. Provide additional
capacity for NKTP and its autoscaling. These requests are not a validated minimum
cluster size. A healthy Metrics Server is required. KWOK is not needed.

## Run

Stop other generators. From the repository root, select your kubeconfig and
install the profile. This replaces release values and sends logs to blackhole.

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
helm dependency build nktp
task install KUBECONFIG_FILE="$KUBECONFIG" \
  HELM_VALUES_FILE=tests/scale/kube-burner-events-values.yaml
```

Wait for NKTP, blackhole and LGTM to be Ready. In a second terminal with the same
kubeconfig, keep this forward running:

```sh
kubectl --kubeconfig "$KUBECONFIG" -n nktp port-forward service/lgtm 9090:9090
```

Run the test:

```sh
RUN_ID="events-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=30m -- \
  -plan "$PWD/tests/scale/plans/kube-burner-events.yaml" \
  -arg run-id="$RUN_ID"
```

Compare CPU, memory, throughput, queues and failures while all 100 producers
are active. Adjust the count and rate in the plan for subsequent runs.

## Cleanup and limitations

Generated Events are retained. Monitor etcd storage and stop before the cluster's
quota is reached. After inspection, remove this run's Events and any producer
Pods left by an interrupted test:

```sh
kubectl --kubeconfig "$KUBECONFIG" -n nktp delete pods \
  -l "scale.nktp.io/workload=kube-burner-events,scale.nktp.io/run-id=$RUN_ID"
kubectl --kubeconfig "$KUBECONFIG" -n nktp delete events \
  -l "scale.nktp.io/workload=kube-burner-events,scale.nktp.io/run-id=$RUN_ID"
```

- The rate is offered load. API throttling and count verification can limit or
  fail a run independently of collector performance.
- Large retained Event populations can cause verification, cleanup and collector
  startup to time out. Verify fresh Event delivery after restarting collectors,
  even when their Pods are Ready.
- Queries record observations without enforcing performance thresholds. Missing
  data is not zero, and blackhole cannot prove unique or durable delivery.
- LGTM storage is temporary. Save results before replacing the environment.
