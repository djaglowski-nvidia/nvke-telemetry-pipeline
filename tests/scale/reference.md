# Scale framework reference

See [setup and available workloads](README.md) for runnable plans. Generator and
measurement configuration lives in the plan. Helm values configure the installed
environment.

## Plans and stages

| Field | Meaning |
| --- | --- |
| `name` | Nonempty plan name |
| `timeout` | Positive execution deadline, including workload startup and recovery |
| `stages` | Stages executed in declaration order |
| `stages[].name` | Unique stage name |
| `stages[].generators` | Parallel generators, each with a unique `name`, `type` and `spec` |
| `stages[].measurement` | Optional observer with a `type` and `spec` |

Generators run concurrently with their stage's measurement. The next stage starts
only after all components finish and their workloads are cleaned up. A component
error cancels its peers and stops the plan, preserving partial results and cleanup
errors. Without a completion check, a measurement finishing does not stop its
generators.

Omit `measurement` for a load-only stage or `generators` for an observation-only
stage. A stage must contain at least one of them. Plan and generator settings are
validated before workloads start. Kubernetes validates native workload fields
when resources are created.

## Generators

| Type | Configuration in `spec` | Completion |
| --- | --- | --- |
| `job` | `podTemplate`, optional `count`, `parallelism`, `completions`, `context` | Every Job completes |
| `pod` | `podTemplate`, optional `count` | Every Pod succeeds |
| `statefulset` | `podTemplate`, positive `duration`, optional `count`, `replicas` | Every set finishes its duration and is Ready |
| `kubernetes` | Positive `duration` and named `nodeGroups` with `replicas` and `nodeTemplate` | All KWOK nodes become Ready and remain for the duration |

`podTemplate` is a native Kubernetes PodTemplateSpec. Put the namespace and labels
in `metadata`, and containers, resources, volumes, scheduling and probes in
`spec`. The namespace defaults to `default` and must already exist. Generated
names and `scale.nktp.io/generator` ownership labels are reserved for the runner.

For Jobs, Pods and StatefulSets, `count` is the number of workload objects. It
defaults to one and accepts zero to create none. Objects run concurrently.
Job `parallelism` defaults to one and `completions` defaults to `parallelism`.
Both must be positive. StatefulSet `replicas` defaults to one and accepts zero.
These fields control Pods per object, independently of `count`.

Jobs and Pods need binaries that exit successfully within the plan deadline.
Jobs default to restart policy `Never` and do not retry failed Jobs. Pods are
created once and are not replaced. StatefulSets require long-lived containers
with restart policy `Always`. Each set's duration starts when all its replicas
first become Ready. Pod replacement does not restart that timer. The plan timeout
also bounds scheduling and recovery.

The `kubernetes` generator creates nodes only. Each node group has a unique
`name`, positive `replicas` (default 1,000), and a native Kubernetes `nodeTemplate`.
Set capacity under `status.capacity`. Allocatable resources default to capacity.
KWOK simulates resources and lifecycle without running containers. The
[KWOK example](examples/kwok/README.md) combines node and Pod generators.

All Kubernetes generators use the plan's kubeconfig. Job `context` can select
another context in that file. They remove their owned resources before returning,
including after partial startup, failure or cancellation. Cleanup has its own
bounded timeout and can extend past the plan deadline. Allow for it in
`TEST_TIMEOUT`. Cleanup failures fail the run.

## Arguments and attribution

Plans use Go `text/template`. Supply values with repeated `-arg NAME=VALUE`
flags after `--`, then reference them as `{{ .Args.name }}`. Hyphenated names
become camel case, so `sample-rate` becomes `.Args.sampleRate`. Missing referenced
arguments and duplicate normalized names fail the command.

`-arg run-id=example-123` supplies `.Args.runId`. Omit it for a fresh ID printed
in the report. IDs accept 1 to 63 ASCII letters, digits, underscores, hyphens or
dots, with a letter or digit at each end. Use the same ID in generator attributes
and query filters to distinguish runs. Pipeline-wide queries can still include
background traffic.

Quote template references that must remain YAML strings and escape supplied
values for the surrounding YAML or query syntax. To emit a literal reference,
use `{{ "{{ .Args.name }}" }}`.

## Prometheus measurements

Use measurement `type: prometheus` with these fields in `spec`:

| Field | Meaning |
| --- | --- |
| `endpoint` | HTTP or HTTPS Prometheus URL reachable from the test host |
| `duration` | Positive collection window starting with the stage |
| `interval` | Polling interval, default `15s` |
| `query_timeout` | Timeout per query, default `10s` |
| `measurements` | Nonempty list of named query or check entries |

Each entry has a unique `name`, optional `units`, and exactly one of `query` or
`check`. Queries contain PromQL and report observations without deciding an
outcome. Reports include per-series statistics and histograms, with missing data,
warnings and errors shown separately. Units `bytes` and `bytes/s` scale the
display automatically. Collection errors fail the plan and retain partial data.

Checks contain `expr`, optional `for` and optional `action`. Expressions must
return instant vectors. Every returned series is a match, including a zero
value. Use filtering comparisons such as `up == 0`, without `bool`. An omitted
`for` acts immediately. Otherwise the same labels must remain present at every
evaluation for that duration, which must be shorter than the collection window.
Missing series reset their timers. Empty results do not fire failure checks.
Use an explicit absence expression when missing telemetry should fail.

`action: fail` is the default and stops the plan when a condition holds.
`action: complete` stops the stage's generators and advances after cleanup. Only
one completion check is allowed per measurement. It must match a single finite
float series with stable labels. Empty results cannot complete a stage.

With a completion check, `duration` is the maximum wait. An unmet target or a
generator finishing before it fails the stage, so keep generators running longer
than that window. Failure checks, query errors, cleanup errors and caller
cancellation take precedence over completion. PromQL lookback windows can contain
samples from earlier stages, so completion conditions should verify fresh data
and the intended load.
