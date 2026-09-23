# Scale Tests

Plan-driven testing of NKTP correctness and capacity under telemetry load.
The harness runs no-op components without a cluster, or finite binary workloads
through the Kubernetes Job generator. The Kubernetes emulator creates temporary
KWOK nodes with configured capacity. Measurements support no-op observations and
Prometheus queries. Query reports collect data without performance pass/fail
verdicts. Successful Job execution alone does not verify backend delivery or NKTP
performance.

## Local cluster

Requires Docker, Kind, kubectl, Helm 3 or 4, and Task. From the repository root:

```sh
task scale:up
task scale:down
```

`scale:up` creates `nktp-scale` with the [dedicated Kind config](kind-config.yaml)
if needed, installs cert-manager and its CRDs, installs KWOK, then installs or
upgrades the NKTP Helm release in namespace `nktp` from the local chart. It prepares chart dependencies and waits
for nodes and NKTP workloads, including workloads created by the Operator. Each readiness wait has a
five-minute timeout. Workload readiness does not verify telemetry delivery.
Chart dependencies are downloaded only when missing or invalid. Once workloads
are ready, `scale:up` forwards [Prometheus](http://127.0.0.1:9090) and
[Grafana](http://127.0.0.1:3000) in the background. Keep local ports 9090 and 3000
free. Repeating `scale:up` restarts both forwards. Logs are in
`tests/scale/.prometheus-port-forward.log` and `.grafana-port-forward.log`.

`scale:up` installs KWOK v0.8.0 using the upstream `kwok` and `stage-fast`
Helm charts at version 0.3.0. It waits for the controller and applies the pod
lifecycle policy. Repeating the command upgrades the releases and reapplies the
policy. The controller is not pinned to the control plane. It manages nodes
annotated `kwok.x-k8s.io/node: fake` and simulates node and pod status without
running containers. The [node generator](#kwok-nodes) creates tainted emulated
nodes. The [Pod generator](#pods) runs workloads on those nodes.

Label emulated pods `scale.nktp.io/lifecycle: complete-30s` to use the
[30-second completion policy](kwok/stages/pod-complete-30s.yaml). It marks a
Running pod `Succeeded` 30 seconds after `status.startTime`. Controller delays
may extend this lifetime. Delete completed pods after collecting their results.
Pods without the lifecycle label use KWOK's default behavior.

Use `HELM_VALUES_FILE=/absolute/path/to/values.yaml` to supply Helm values.
Relative paths are resolved from `tests/scale/`. Each run applies the current
chart defaults and the supplied values file. Pass the file on every run that
needs it. Omitting it restores chart defaults. Tilt is not required.

Run `scale:up` again to apply local chart or values changes to the existing
cluster. Kind configuration changes require `scale:down` followed by `scale:up`.
Failed setup leaves the cluster for inspection. After correcting the problem,
retry `scale:up` or run `scale:down` followed by `scale:up` for a fresh cluster.

Scale commands use only `tests/scale/.kubeconfig` and the `kind-nktp-scale`
context. For manual access:

```sh
export KUBECONFIG="$PWD/tests/scale/.kubeconfig"
```

`scale:down` stops both managed port-forwards, then deletes the scale cluster and
its kubeconfig. It does not stop the dev Tilt session or delete the dev cluster.
The no-op plans below do not require `scale:up`.

## Domain model

| Concept | Meaning |
| --- | --- |
| `Plan` | Named configuration with a timeout, execution mode, and ordered stages. |
| `Stage` | Unique name and one generator/measurement pair. The pair runs concurrently. |
| `ComponentSpec` | Implementation `type` and its implementation-specific `spec`. |
| Generator | Runnable responsible for the complete generation lifetime and resource cleanup. |
| Measurement | Runnable that produces findings and returns an error if execution or checks fail. |
| `measurements.Report` | Measurement-owned structured findings. An empty report is valid. |
| `PlanReport` | Run ID, plan name, and stage reports with component errors, in declaration order. |

The plan contains configuration, not executable objects. `Executor` validates
every component specification and constructs fresh runnables before any stage
starts. Generators support `type: noop`, `type: job`, `type: pod` and
`type: kubernetes`.
Measurements support `type: noop` and `type: prometheus`. No-op components accept an empty or omitted
`spec`. No-op measurements also accept `spec.queries`, a mapping of names to nonempty query
strings. These strings are resolved by the plan parser but are not evaluated.
No-op components return ordinary success and empty measurement reports. This
does not evaluate NKTP correctness or performance.

```mermaid
flowchart TD
    Flags["Test flags"] --> Arguments["Resolve arguments and default run ID"]
    Arguments --> Parse["Substitute arguments and validate structure"]
    YAML["Plan YAML"] --> Parse
    Parse --> Plan["Plan: stages, execution, timeout"]
    Plan --> Executor["Executor: validate specs and construct runnables"]
    Executor --> Schedule["Parallel stages by default, sequential optional"]
    Schedule --> Stage["Each stage"]
    Stage --> Generator["Generator.Run"]
    Stage --> Measurement["Measurement.Run"]
    Generator --> Join["Wait for both and retain errors"]
    Measurement --> Join
    Join --> Reports["Consolidate reports after all stages finish"]
```

## Basic configuration

The following plan runs two independent stages concurrently:

```yaml
name: harness-smoke
execution: parallel
timeout: 1m
stages:
  - name: baseline
    generator:
      type: noop
      spec: {}
    measurement:
      type: noop
      spec: {}
  - name: second-stage
    generator:
      type: noop
      spec: {}
    measurement:
      type: noop
      spec: {}
```

This example is available at [plans/noop.yaml](plans/noop.yaml). Omitted
`execution` defaults to `parallel`. Set it to `sequential` to finish each stage
before starting the next. The generator and measurement still run concurrently
within each stage. A positive `timeout` and unique, nonempty stage names are
required. Unknown envelope fields, duplicate envelope keys, and multiple YAML
documents are rejected by the parser. Implementation names and specs are checked by the
executor before any runnable starts.

## Running

From the repository root, run the telemetrygen plan against the default scale
Kind cluster:

```sh
task scale:up
task test:scale -- -plan "$PWD/tests/scale/plans/telemetrygen.yaml"
task scale:down
```

The default kubeconfig is `tests/scale/.kubeconfig`, created by `scale:up` for
context `kind-nktp-scale`. To use another cluster:

```sh
task test:scale KUBECONFIG=/absolute/path/to/kubeconfig -- -plan "$PWD/tests/scale/plans/telemetrygen.yaml"
```

Override the default with the Task variable `KUBECONFIG` before `--`, an
environment variable, or `-kubeconfig PATH` after `--`. The forwarded form
`-plan PATH KUBECONFIG=/absolute/path/to/kubeconfig` also works. A trailing
assignment takes precedence over `-kubeconfig`, which overrides the Task or
environment default. Prefer absolute paths for overrides. Relative paths
resolve from `tests/scale`. Kubeconfig file lists are rejected.

No-op plans do not require a kubeconfig or cluster:

```sh
task test:scale
task test:scale -- -plan "$PWD/tests/scale/plans/noop.yaml"
```

Or from `tests/`:

```sh
go test -tags scale -count=1 -v -timeout=12m -run '^TestRun$' ./scale
go test -race -tags scale -count=1 ./...
```

`tests/scale/main_test.go` is the build-tagged entrypoint. It resolves arguments,
loads the plan and calls `runner.Executor.Execute`. Plan paths are absolute or relative to
`tests/scale`, the Go test's working directory. The executor uses Go's default
`slog` logger to log each stage's start and completion, then the plan's completion,
with plan name, run ID, and errors. Consolidated stage reports are printed after
execution, with summary statistics and numeric histogram counts for each query
result series. Any returned error fails the test and command.

## Arguments and attribution

Scale tests can send telemetry to the same backend as other tests or earlier
runs. Attribution lets you distinguish one run's metrics, logs and traces from
that other traffic. Use the same run ID in generator attributes and measurement
queries to select the intended test data.

Use `{{args.name}}` to make a value configurable. For example, in your plan's
Job spec:

```yaml
args:
  - "--rate={{args.rate}}"
  - "--duration={{args.duration}}"
  - '--telemetry-attributes=test_run_id="{{args.run-id}}"'
```

Run that plan from the repository root, supplying values with repeated
`-arg NAME=VALUE` flags:

```sh
task test:scale -- -plan /absolute/path/to/plan.yaml \
  -arg rate=100 \
  -arg duration=3m \
  -arg run-id=ci-123
```

Supply each referenced argument once, except `run-id`, which is generated when
omitted. Other missing arguments and duplicate assignments fail the command.
Argument names start with an ASCII letter and may contain
letters, digits, underscores and hyphens.

Omit `-arg run-id=...` to get a fresh random ID for each run, shown in the test
output. Supply your own ID to associate a run with a CI job or another external
reference. Use 1 to 63 ASCII letters, digits, underscores, hyphens or dots, with a
letter or digit at each end. Use a different ID for each run you want to
distinguish.

Reference `{{args.run-id}}` in query filters too, for example
`count({test_run_id="{{args.run-id}}"})`. The
[telemetrygen plan](plans/telemetrygen.yaml) includes examples for metrics, logs
and traces. The metrics stage executes PromQL queries against the in-cluster
Prometheus. The logs and traces stages retain no-op query examples.

Quote references that should remain strings, as in the YAML example. Escape
supplied values for the surrounding YAML and query syntax. To include the
literal text `{{args.name}}`, write `{{{{args.name}}`.

## Prometheus measurements

Prometheus measurements collect metrics through PromQL queries during a load test
and summarize the results without performance pass/fail thresholds.

```yaml
measurement:
  type: prometheus
  spec:
    endpoint: http://127.0.0.1:9090
    duration: 3m
    interval: 15s
    query_timeout: 10s
    units:
      run_series: series
      throughput: points/s
      queue: bytes
    queries:
      run_series: 'count({test_run_id="{{args.run-id}}"})'
      throughput: 'sum(rate(otelcol_exporter_sent_metric_points_total{job="nktp-gateway-metrics"}[2m]))'
      queue: 'otelcol_exporter_queue_size{job="nktp-gateway-metrics",data_type="metrics"}'
```

The [telemetrygen plan](plans/telemetrygen.yaml) measures series from the current
run, plus pipeline throughput, queues, and memory across all traffic.

An `endpoint`, a positive `duration`, and at least one named query are required.
`interval` defaults to `15s` and `query_timeout` to `10s`. Optional `units` labels
results by query name, with automatic scaling for `bytes` and `bytes/s`.

For the scale Kind cluster, `task scale:up` makes Prometheus available at the
example endpoint. For another environment, use an endpoint reachable from the
test host.

Collection starts with the stage, querying at `interval`, and ends normally after
`duration`. Query failures before that deadline fail the run and preserve results
collected so far. The stage waits for both collection and load generation to
finish. The test report shows statistics and histograms for each query and label
set, with missing data and errors shown separately.

## Execution and extension

The [pkg](pkg) directory contains the `runner` Go package within `tests/go.mod`.
`plan.go` owns configuration and parsing. `arguments.go` resolves invocation
flags. `executor.go` owns consumer-defined runnable interfaces, scheduling,
and consolidated reports. `components.go` selects
constructors and delegates spec validation to them.

Concrete generators live in [pkg/generators](pkg/generators), and concrete
measurements live in [pkg/measurements](pkg/measurements). Each package's `noop.go`
provides a `Noop` implementation, a `NewNoop` constructor, and private spec
validation as the example for future implementations: validate the spec before
returning a runnable, without acquiring resources.

`measurements.Report` holds the measurement's findings. `StageReport` and
`PlanReport` stay with the executor. Both implementation packages satisfy the
executor's interfaces without importing the runner package. `main_test.go`
imports `tests/scale/pkg` as `runner`.

A component error cancels its peer in the same stage. Other stages continue,
including later sequential stages. Successful completion does not cancel a peer.
Plan timeout or caller cancellation stops active work and prevents unstarted
components from running. Their report entries contain cancellation errors.
Partial reports are retained, and report order is independent of completion order.

Every runnable must honor its context and finish its goroutines and bounded
cleanup before returning, including after partial startup. The executor waits
for these returns. Cancellation cannot forcibly stop an uncooperative runnable.
Constructors must acquire no resources. Measurements must finish modifying their
reports before returning them.

The harness can be extended with additional generators and measurements.

## Available generators

- **No-op (`noop`)** exercises the harness without generating load or requiring a
  cluster. See the [no-op plan](plans/noop.yaml).
- **Kubernetes Pod (`pod`)** runs a batch of native Pod templates on real or
  emulated nodes. See the [KWOK Pod plan](plans/kwok-pods.yaml).
- **Kubernetes Job (`job`)** runs a containerized load generator to completion.
  See the [telemetrygen plan](plans/telemetrygen.yaml) or
  [Avalanche plan](plans/avalanche.yaml) for configuration.
- **Kubernetes emulator (`kubernetes`)** creates KWOK nodes, holds them while
  measurements run, then deletes them. See the [node plan](plans/kwok-nodes.yaml).

The telemetrygen plan uses namespace `nktp` and the NKTP services installed by
`task scale:up`. When using another cluster, adjust the plan namespace and
endpoints as needed. The test harness itself does not provision a cluster.

Jobs spread their pods evenly across eligible nodes. Each Job spreads
independently, with pod counts differing by at most one. Pods remain Pending when
the placement constraint cannot be satisfied. Running pods are not automatically
rebalanced.

**Jobs and pods are cleaned up before the test returns**, including after failure
or cancellation. Workload and cleanup errors fail the test.

## KWOK nodes

The Kubernetes emulator creates temporary nodes in named groups, each with its
own Kubernetes node template. KWOK simulates their capacity without allocating
real CPUs or memory.

```yaml
generator:
  type: kubernetes
  spec:
    duration: 10m
    nodeGroups:
      - name: workers
        replicas: 1000
        nodeTemplate:
          metadata:
            labels:
              run_id: '{{args.run-id}}'
          status:
            capacity:
              cpu: "96"
              memory: 384Gi
              pods: "110"
```

Each group defaults to 1,000 replicas. `nodeTemplate` uses native Kubernetes Node
fields for labels, annotations, spec and status. Set CPU, memory and pod capacity
in `status.capacity`. Other resources, such as `nvidia.com/gpu`, are also supported.
Allocatable resources default to capacity. Add groups with different templates to
represent different node types or zones.

The generator uses the plan's kubeconfig. It waits for all groups to become Ready,
keeps them available for `duration`, then removes them. Duration must be positive.
Cleanup also runs after failure or cancellation. Nodes are removed concurrently,
with a separate cleanup timeout that grows with the number of nodes. Cleanup
failures are reported alongside run failures. This generator creates nodes only.

Kubernetes validates the node templates when they are created. Replica counts
include only generated nodes. The real Kind nodes remain in the cluster, so the
total node count will be higher.

The [example plan](plans/kwok-nodes.yaml) uses this configuration. From the
repository root:

```sh
task scale:up
task test:scale -- -plan "$PWD/tests/scale/plans/kwok-nodes.yaml" -test.timeout=40m
```

The test timeout allows for the plan deadline and cleanup. Increase it when
using larger groups or a longer plan timeout.

## Pods

The Pod generator runs a finite batch from a native Kubernetes Pod template.
The [KWOK example](plans/kwok-pods.yaml) simulates 1,000 Pods that complete after
30 seconds of Running time.

```yaml
generator:
  type: pod
  spec:
    replicas: 1000
    podTemplate:
      metadata:
        labels:
          run_id: '{{args.run-id}}'
          scale.nktp.io/lifecycle: complete-30s
      spec:
        restartPolicy: Never
        nodeSelector:
          run_id: '{{args.run-id}}'
        tolerations:
        - key: kwok.x-k8s.io/node
          operator: Equal
          value: fake
          effect: NoSchedule
        containers:
        - name: load
          image: registry.k8s.io/pause:3.10
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
```

`task scale:up` installs KWOK and the completion policy. Target KWOK nodes must
have the matching `run_id` label and enough capacity for the requested Pods.
Use the same kubeconfig and run ID for the node and Pod generators:

```sh
task test:scale -- -plan "$PWD/tests/scale/plans/kwok-pods.yaml" -arg run-id=example-run -test.timeout=30m
```

The test timeout allows for the plan's 20-minute deadline and cleanup.

Replicas default to one, and zero creates no Pods. The namespace comes from
`podTemplate.metadata.namespace`, defaults to `default`, and must already exist.
Pods start as scheduling permits, including when nodes become available later.
The run succeeds when all Pods succeed. Failure, disappearance or plan timeout
fails the run. Pods are created once and are not replaced. Names and the
`scale.nktp.io/generator` label are assigned for cleanup.

KWOK simulates the lifecycle without executing the image. To run a real load
binary, remove the KWOK lifecycle label, selector and toleration, then set the
container image, command and arguments. The binary must exit successfully within
the plan timeout. All other native template settings are preserved.

## Avalanche

[Avalanche](https://github.com/prometheus-community/avalanche) generates
Prometheus metrics for NKTP to scrape. The [example plan](plans/avalanche.yaml)
runs three independent pods for three minutes. Each pod serves 10,000 gauge
series. The [scrape profile](scale-test-values.yaml) routes Avalanche targets
through NKTP's two large-target collectors and the metrics gateway to the
blackhole collectors.

From the repository root:

```sh
task scale:up HELM_VALUES_FILE=scale-test-values.yaml
task test:scale -- -plan "$PWD/tests/scale/plans/avalanche.yaml"
```

Pass the values file on subsequent `scale:up` runs to keep the scrape profile.
The plan and profile use namespace `nktp`. Adjust both when using another namespace.

Job `parallelism` defaults to `1` and must be a positive integer. It requests
concurrent pods and requires that many successful completions. Pods start
independently as resources become available. `pod_labels` supplies labels for
scrape discovery. Job controller and harness ownership labels are reserved.

Adjust the plan's parallelism or Avalanche arguments to change the load.
The measurement is currently no-op, so successful completion confirms workload
execution and cleanup without verifying backend delivery.

**Limitation:** Avalanche runs as a Kubernetes Job and does not preserve
time-series identity across pod replacement. If a pod is replaced during a test,
its new labels create distinct series that may overlap with the previous pod's
series in backend queries. This can inflate the observed series count or mask
missing data, so the count alone does not prove that the expected number of
series is being received simultaneously. StatefulSet support with stable replica
identities and label mapping is planned.

## Continuous metrics load

[plans/metrics-1m.yaml](plans/metrics-1m.yaml) runs upstream Avalanche and
telemetrygen concurrently in the kind cluster for ten minutes. It targets
approximately **1 million distinct series offered per rolling 60-second window**
after startup.

| Generator | Workloads | Delivery to NKTP | Target series per window |
| --- | --- | --- | --- |
| Avalanche | 1 Job with 5 pods, each serving 100,000 series | Two large-target collectors scrape every 15 seconds | 500,000 |
| telemetrygen | 1 Job with 3 pods, each running 128 workers | Continuous OTLP stream of new series | About 507,000 |

From the repository root:

```sh
task scale:up HELM_VALUES_FILE=scale-test-values.yaml
task test:scale -- -plan "$PWD/tests/scale/plans/metrics-1m.yaml"
```

[plans/metrics-2m.yaml](plans/metrics-2m.yaml) doubles the offered load to about
2 million series per rolling 60-second window using 10 Avalanche pods and
6 telemetrygen pods. It uses the same ten-minute duration and scale profile.

```sh
task test:scale -- -plan "$PWD/tests/scale/plans/metrics-2m.yaml"
```

The [scale profile](scale-test-values.yaml) runs two blackhole collectors that
accept and discard traffic from all three gateways. Watchdog keeps component and
producer observations in LGTM. Generator pods spread evenly across the three
kind workers.

The report covers resource use, gateway and blackhole throughput, queues and
scrape health. Compare results after all generators have started and produced
traffic for at least one minute. Jobs and pods are cleaned up automatically.

Limitations:

- The target is approximate and received unique-series counts are not verified.
  Generated telemetry is discarded. Series-count queries in other plans require
  the normal LGTM setup without this profile.
- Throughput counters include background traffic and may include retries. Run
  one load test at a time when comparing the measurements with the target.
- Watchdog outages can cause missing scrape diagnostics and collection errors.
- All kind nodes share the Docker VM's resources. Even placement adds no CPU or
  memory. The blackhole also consumes resources and can limit throughput.
  A measurement timeout stops its paired generator.
- Spreading balances pod counts, not resource use. Insufficient capacity can
  leave pods Pending, and node failures do not trigger automatic rebalancing.
- Telemetrygen's [upstream throughput issue](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/49950)
  can reduce the load during a run.
- Telemetrygen must connect directly to the metrics gateway with its source
  address preserved. An intermediary that hides client addresses can merge
  identities. Pod replacement or address reuse can also distort the load.
