# Kubernetes Events

- [Plan](plans/kube-events.yaml) · [Profile](kube-events-values.yaml)
- Load: 100 k6 Pods, one per worker, offering **10,000 Event updates/s** for two minutes
- Each Pod creates 100 Events with 1 KiB messages, then patches them at 100/s
- Total: 10,000 retained objects, 1.2 million updates and five minutes of observations
- Path: Events collector → logs gateway → blackhole, with NKTP default resources

k6 patches known Event names directly, avoiding kube-burner's repeated discovery
LIST requests over large Event populations. It provides controlled request pacing
and counters for successful requests, failures and dropped iterations.

Requires 100 workers labeled `nodeGroup=customer-cpu`, system nodes labeled
`nodeGroup=system-cpu`, and a healthy Metrics Server. Adapt placement and API
access to your cluster. Blackhole and LGTM request 24 CPU and 32 GiB together,
plus capacity for NKTP. Workers must reach LGTM on port 9090 for k6 metrics.

## Run

Stop other generators and follow the [shared setup](README.md#setup).
From the repository root:

```sh
export KUBECONFIG="/absolute/path/to/kubeconfig"
task scale:install KUBECONFIG="$KUBECONFIG" INSTALL_KWOK=false \
  HELM_VALUES_FILE=tests/scale/kube-events-values.yaml
RUN_ID="events-$(date -u +%Y%m%d%H%M%S)"
task scale:test KUBECONFIG="$KUBECONFIG" TEST_TIMEOUT=30m -- \
  -plan "$PWD/tests/scale/plans/kube-events.yaml" -arg run-id="$RUN_ID"
```

## Results

Compare successful PATCHes with singleton acceptance while producers are active.
Acceptance includes background Events. Require zero failed requests and dropped
iterations, then inspect CPU, memory, queues, refusals and restarts. Configured
rate alone does not establish delivery. Missing metrics are not zero.

k6 deletes its Events on exit, with Pod ownership providing garbage collection
after interruption. The harness removes generator Pods. Keep LGTM for review.
