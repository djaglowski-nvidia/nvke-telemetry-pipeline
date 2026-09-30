# NKTP Helm Chart

NKTP is how NVIDIA runs OpenTelemetry on Kubernetes.

## Prerequisites

Kubernetes 1.25+, per the bundled OpenTelemetry Operator's
[compatibility matrix](https://github.com/open-telemetry/opentelemetry-operator/blob/v0.159.0/docs/getting-started/compatibility.md).

## OpenTelemetry Topology

TODO

### Image registry

Set `global.imageRegistry` to override the image registry of all the components,
including the Collectors (`otelcol_image`) and the Target Allocators
(`targetallocator_image`). Caveat: `metrics-server` dependency and the nested OpenTelemetry Operator do **not**
read this setting. Configure their image repositories explicitly when required.


### Secure backend export

The default LGTM backend uses plaintext OTLP for local development. Configure an
HTTPS endpoint before sending gateway traffic to a production backend. See
[Secure gateway export](../docs/gateway_mtls.md) for TLS, mTLS, and cert-manager
configuration.

### Internal collector TLS

By default, NKTP uses cert-manager to create a self-signed CA, issuer and certificates for each collector 
used for collector-to-collector traffic encryption. Setting `tls.internal.certManager.issuerRef` allows you to specify
an existing `Issuer` or `ClusterIssuer`.

Disabling `tls.internal` requires you to manually reconfigure the collector configs to use `http` endpoints and not include `tls` blocks.

### Resource sizing

Default budgets are per Pod. Configure collector resources under
`<component>.kubernetes.resources` and allocator resources under
`<component>.kubernetes.targetAllocator.resources`.

| Component | CPU / memory request | CPU / memory limit |
| --- | --- | --- |
| Node collector | 1 / 2 GiB | 2 / 4 GiB |
| Large-target collector | 2 / 8 GiB | 4 / 16 GiB |
| Metrics, logs and traces gateways | 200m / 512 MiB | 4 / 8 GiB |
| Cluster event reader | 1 / 512 MiB | 4 / 1 GiB |
| Watchdog | 500m / 512 MiB | 4 / 1 GiB |
| Node Target Allocator | 250m / 2 GiB | 2 / 4 GiB |
| Large-target and watchdog Target Allocators | 250m / 1 GiB | 1 / 2 GiB |

Node collectors use a `3276MiB` `GOMEMLIMIT`. Gateways use 7 GiB,
large-target collectors use 8 GiB and watchdog uses 768 MiB. Tune runtime memory
targets and queues together with container limits.
The node metrics queue holds 32 MiB to admit larger scrapes before batching.

HPA targets are relative to requests. Increasing limits does not raise the
scaling threshold. Reserve node capacity for growth and failure recovery, and
start enough scraper replicas to absorb large initial loads. These budgets do
not guarantee a particular series count. Validate allocator discovery and
reassignment under load, especially after restarts.

### Watchdog

The watchdog receives internal metrics, logs and traces from NKTP collectors over
OTLP/HTTP at `http://nktp-watchdog-collector:4318` in the Helm release namespace.
It forwards received telemetry and exports its own telemetry to LGTM by default.

The bundled OpenTelemetry Operator manages the watchdog. Autoscaling requires the
Kubernetes resource metrics API, commonly provided by metrics-server.
Setting `watchdog.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.

Configure the Collector through `watchdog.otelcol.config`.

Use `watchdog.kubernetes` for Collector CR settings and `watchdog.otelcol.image`
to override the global `otelcol_image`.
Include `GOMEMLIMIT` and `GOGC` in any replacement `watchdog.kubernetes.env` list
to retain these settings.

### Logs gateway

The logs gateway accepts OTLP logs over gRPC at `nktp-gateway-logs-collector:4317`
and HTTP at `http://nktp-gateway-logs-collector:4318` in the Helm release namespace.
It forwards received logs to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_logs.otelcol.config` for Collector configuration and
`gateway_logs.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_logs.kubernetes.env`.

### Metrics gateway

The metrics gateway accepts OTLP metrics over gRPC at `nktp-gateway-metrics-collector:4317`
and HTTP at `http://nktp-gateway-metrics-collector:4318` in the Helm release namespace.
It forwards received metrics to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_metrics.otelcol.config` for Collector configuration and
`gateway_metrics.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_metrics.kubernetes.env`.

### Traces gateway

The traces gateway accepts OTLP traces over gRPC at `nktp-gateway-traces-collector:4317`
and HTTP at `http://nktp-gateway-traces-collector:4318` in the Helm release namespace.
It forwards received traces to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_traces.otelcol.config` for Collector configuration and
`gateway_traces.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_traces.kubernetes.env`.

### Cluster events

The cluster event reader sends Kubernetes Event logs to the logs gateway. Two
replicas share one watch through leader election. Its own metrics, logs and
traces go to watchdog.

Use `cluster_events.otelcol.config` for Collector configuration and
`cluster_events.kubernetes` for Kubernetes workload configuration. Retain
`GOMEMLIMIT`, `GOGC` and `POD_NAMESPACE` when replacing its environment list.
Set `receivers.k8s_events.namespaces` within the Collector configuration to limit
the watched namespaces. Events during a leadership handoff can be missed.

The chart creates the election Lease in the release namespace and restricts the
reader's Lease permissions to that name. Set `extensions.k8s_leader_elector.lease_name`
in the Collector configuration to change the name. Use a literal name so Helm can
create the matching Lease. If using another Lease namespace, provision the Lease
and its RBAC there separately.

### kube-state-metrics

The DaemonSet under `kube-state-metrics.daemonset` collects scheduled Pods on its node.
Configure central workloads under `kube-state-metrics.deployments`. Each central workload's `resource_types`
selects the Kubernetes resource types to collect and grants matching RBAC.
Set `track_unscheduled_pods: true` to collect Pods without a node assignment.
This is supported only for central workloads and requires `resource_types` to be exactly `[pods]`.
Resource names come from the [upstream resource list](https://github.com/kubernetes/kube-state-metrics/blob/v2.20.0/docs/README.md#exposed-metrics).

`mutatingadmissionpolicies` and `mutatingadmissionpolicybindings` are disabled by
default because KSM uses their `admissionregistration.k8s.io/v1` APIs, which older
clusters do not serve. Add them to `resource_types` only when the cluster serves both APIs.

`kube-state-metrics.namespaces` sets the namespace scope unless a workload supplies
its own `namespaces` list. RBAC follows the effective namespace selection.
An empty list means all namespaces. Empty strings are ignored.

Set `kube-state-metrics.metricLabelsAllowlist` to expose selected Kubernetes labels,
for example `["pods=[app.kubernetes.io/name]"]`. Additional Kubernetes labels are
disabled by default.

Secret metrics are opt-in because collection requires list/watch access to complete
Secret objects, including their contents. To enable them, set
`kube-state-metrics.deployments.configuration.resource_types` to `[configmaps, secrets]`.
Set that workload's `namespaces` list to limit access. An empty list grants access
across the cluster.

Shared `kubernetes` defaults can be overridden under each workload. `resources`,
`env` and `securityContext` configure the KSM container, `podSecurityContext`
configures Pod security, and remaining fields configure the Pod spec.
Set `serviceAccount` to use an existing account.

When overriding scheduling settings, ensure the node Collector runs on every node
where the KSM DaemonSet runs so it can scrape the local KSM endpoint.

Increase `shards` when a central workload exceeds its memory or scrape response
budget. Helm supplies `--shard` and `--total-shards`. Each shard runs as one
Deployment and briefly stops collection during replacement.

### Node collector

The node Collector accepts OTLP logs, metrics, and traces at
`nktp-daemonset-collector:4317` over gRPC and `http://nktp-daemonset-collector:4318`
over HTTP. The Service routes traffic only to the Collector on the sender's node.
If no local Collector is ready, traffic fails rather than reaching another node.
The Collector sets `k8s.node.name` and forwards each signal to its corresponding
gateway. Qualify the Service name with the NKTP namespace when
sending from another namespace.

The DaemonSet reads pod logs from `/var/log/pods` and sends them to the logs
gateway. A dedicated Target Allocator assigns monitored metric endpoints only
to collectors on the same node. Metrics go to
`http://nktp-gateway-metrics-collector:4318`, which requires the metrics gateway.
The `hostmetrics` receiver reads host `/proc` through a read-only mount at
`/hostfs/proc`. Only its `process` scraper is enabled. It collects per-process
CPU time, physical and virtual memory, and disk I/O every minute, and sends them
through the metrics gateway. Full process command lines are not exported.
Restricted processes can have missing metrics or attributes and produce scrape
errors.
Node exporter remains responsible for other host metrics.

Each DaemonSet collector also scrapes the kubelet `/metrics` endpoint on its own
node over HTTPS every 30 seconds. The collector connects directly to the node's
host IP on port 10250; this scrape does not use a ServiceMonitor or the Target
Allocator. It authenticates with the collector's projected service-account token
and has read-only `nodes/metrics` permission. The certificate authorities that
sign kubelet serving certificates, and the subject alternative names in those
certificates, vary between Kubernetes distributions, so certificate verification
is disabled by default. To enable it, create a ConfigMap in the NKTP release
namespace containing the kubelet-serving CA:

```console
kubectl create configmap kubelet-serving-ca \
  --namespace <release-namespace> \
  --from-file=ca.crt=/path/to/kubelet-serving-ca.crt
```

Then configure its name and key:

```yaml
daemonset:
  kubelet:
    tls:
      ca:
        configMap:
          name: kubelet-serving-ca
          key: ca.crt
```

The chart mounts the selected key into the collector and merges the required
settings into the `kubelet` job's `tls_config`. It preserves other TLS fields,
but this option takes precedence for `ca_file` and sets `insecure_skip_verify`
to `false`.

Each kubelet certificate must cover the node IP used by the collector.

By default, each collector also scrapes `/metrics/cadvisor`. It applies default
[metric relabelings copied from kube-prometheus-stack to drop less useful,
always-zero, overlapping, host-network-interface, and podless-cgroup
samples](https://github.com/prometheus-community/helm-charts/blob/4713c624771a48e99f16dec9eb55ec8204f06830/charts/kube-prometheus-stack/values.yaml#L1952).
Set `daemonset.kubelet.cadvisor.metricRelabelings` to replace these filters.
Disable cAdvisor or enable `/metrics/probes` and `/metrics/resource`
independently:

```yaml
daemonset:
  kubelet:
    cadvisor:
      enabled: false
    probes:
      enabled: true
    resource:
      enabled: true
```

Each optional job inherits the `/metrics` job's connection, authentication, and
resolved TLS configuration.

When kube-prometheus-stack is installed alongside NKTP, disable its kubelet
ServiceMonitor only after enabling the required replacement endpoints in NKTP
and migrating their consumers. This avoids scraping kubelet `/metrics` twice:

```yaml
kubelet:
  serviceMonitor:
    enabled: false
```

As a best-effort fallback, the DaemonSet Target Allocator excludes
ServiceMonitors with the conventional
`app` labels `kube-prometheus-stack-kubelet` and
`prometheus-operator-kubelet` to avoid unintentionally assigning an externally
managed kubelet scrape. ServiceMonitor names and labels are configurable, so
this fallback is not guaranteed to identify every kubelet ServiceMonitor.

The DaemonSet also collects systemd journal entries at info priority and above
and forwards them to the logs gateway. Nodes must provide `/usr/bin/journalctl`
and `/etc/machine-id`, with journals in `/var/log/journal`. The Collector uses
selective read-only host mounts and
the `SYS_CHROOT` capability to run the host executable. The `/lib64` host volume
is also mounted at `/hostfs/usr/lib64` for Red Hat library paths. Collection
starts with new entries. Journal cursors are not persisted across Collector restarts.

Override `root_path`, `journalctl_path`, and `directory` under
`daemonset.otelcol.config.receivers.journald` for a different host layout.
Update `daemonset.kubernetes.volumes` and `volumeMounts` to match, retaining
the other mounts when replacing either list. For runtime journals, add a
`/run/log/journal` host volume mounted at `/hostfs/run/log/journal` and set the
receiver directory to `/run/log/journal`.

See [Log classification](#log-classification) for the default audit and
control plane log sources.

The collector's own metrics, logs and traces go to watchdog.
Target Allocator telemetry is sent to watchdog.
Each collector also reads Target Allocator stdout and stderr logs on its node
in the release namespace and sends them to watchdog through `logs/targetallocator`.
Operator manager container logs go to watchdog through `logs/operator`.

Use `daemonset.otelcol.config` for Collector configuration and
`daemonset.kubernetes` for Kubernetes workload configuration. The pod-log mount
is read-only, but host file permissions require the collector to run as root.
The file reader follows new records and does not persist offsets across restarts.
Retain `GOMEMLIMIT` and `GOGC` when replacing its environment list.
The chart adds `NODE_NAME`, `POD_NAMESPACE`, and `OTEL_K8S_NODE_IP` if the list
omits them.

Setting `daemonset.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.

Monitoring CRs labeled `nktp.nvidia.io/large-target: "true"` are excluded.
Unlabeled monitors remain eligible. Targets without a matching collector node
remain unassigned.

The Target Allocator rejects ServiceMonitor and PodMonitor endpoints that reference
local bearer-token or TLS files in the Collector container.

To grant the allocator access to Secrets and CA ConfigMaps referenced by monitors, list
the namespaces containing those monitors and their referenced objects:

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      prometheusCR:
        secretNamespaces: [metrics-monitors]
```

`daemonset.rbac.create` defaults to `true`. For each explicitly
listed namespace, Helm creates a Role granting Secrets `get/list/watch` and
ConfigMaps `get`, bound to the allocator ServiceAccount in the release namespace.
The binding honors `daemonset.kubernetes.targetAllocator.serviceAccount`.
Omitting the list or setting it to `[]` creates no namespace RBAC. Set
`daemonset.rbac.create: false` to manage these grants externally.

Grants cover all Secrets in each listed namespace. Use namespaces with trusted
monitor authors. Secret and CA references resolve in the monitor's namespace.
The namespaces must already exist. `secretNamespaces` controls Secret watching.
Configure monitor and namespace selectors separately.

### Log classification

NKTP classifies logs by source using the `nvidia.telemetry.class` resource
attribute. All records from a classified source receive the same class,
regardless of their content. The class is forwarded to the logs gateway for
use in routing rules.

| Source | Classification | `service.name` | Recognition |
| --- | --- | --- | --- |
| sudo and SSH journal entries | `system.auth` | `journald` | Journal producer identifier is `sudo` or `sshd`. |
| Linux authentication files | `system.auth` | `auth` | All records from `/var/log/auth.log` or `/var/log/secure`. |
| Linux auditd | `system.audit` | `auditd` | All records from `/var/log/audit/audit.log`, including authentication records. |
| Kubernetes control plane logs | `k8s.control_plane` | Not set by NKTP | Pods in `kube-system` with matching pod-name prefixes and container names for `kube-apiserver`, `kube-controller-manager`, `kube-scheduler`, or `etcd`. |
| Kubernetes API audit logs | `k8s.api.audit` | `kube-apiserver` | Records from `/var/log/kubernetes/audit/audit.log`. API audit logging must be enabled on the API server. |

Other locally collected logs remain unclassified. All journal records use
`journald` as `service.name`. Shared authentication files use `auth`. Service
names reach the logs gateway alongside the classification.

### Large-target metrics

The large-target metrics pool (`large_target_metrics`) selects ServiceMonitor,
PodMonitor, Probe, and ScrapeConfig resources with the metadata label
`nktp.nvidia.io/large-target: "true"`.
Apply this label to monitoring resources for endpoints with large scrape
responses, such as kube-state-metrics.

A Target Allocator distributes scrapes across the pool's replicas.
The Target Allocator rejects ServiceMonitor and PodMonitor endpoints that reference
local bearer-token or TLS files in the Collector container.
Setting `large_target_metrics.kubernetes.targetAllocator.enabled` to `false`
will cause Helm rendering to fail. Target Allocator telemetry is sent to watchdog.
The allocator has its own security contexts and resource budget under
`large_target_metrics.kubernetes.targetAllocator`. Size its resources for the
discovered Kubernetes objects and targets.

Collected metrics go to `http://nktp-gateway-metrics-collector:4318`.
Each Collector sends its own telemetry to watchdog.
Use `large_target_metrics.otelcol.config` for Collector internal configuration and
`large_target_metrics.kubernetes` for workload configuration.

This pool reserves more memory than the node DaemonSet for large scrape
responses. Autoscaling requires the Kubernetes resource metrics API and
distributes whole targets. An individual endpoint must still fit in one
Collector. Scrape defaults limit each response to 1,000,000 samples and 1 GiB.
Split endpoints that exceed these limits into smaller scrape targets where
possible. If raising the scrape limits, size the exporter queue, memory request
and limit, `GOMEMLIMIT`, and memory limiter thresholds together. Response bytes
do not directly measure the memory needed to parse and export a scrape.
Retain `GOMEMLIMIT` and `GOGC` when replacing environment lists.
For large targets scraped every 60 seconds, consider `scrapeTimeout: 55s` on the
ServiceMonitor or PodMonitor endpoint. Keep the timeout below the scrape interval.

## Backpressure

Collector pipelines use synchronous processors and batch data within exporter
queues. With `block_on_overflow: true`, full queues make incoming calls wait for
capacity, allowing downstream congestion to slow ingestion. The memory limiter
can refuse data under memory pressure, so receivers and upstream senders must
retry retryable failures. The Filelog receiver retries these failures until
recovery and drops errors marked permanent.

Each Collector exports its own telemetry through separate SDK buffering. Logs
and traces use bounded asynchronous queues, and metrics use a periodic reader.
This keeps monitoring congestion from blocking collection.

## Managed Dependencies

### OpenTelemetry and CRDs

NKTP depends on the published [opentelemetry-kube-stack 0.22.0](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/opentelemetry-kube-stack-0.22.0/charts/opentelemetry-kube-stack)
chart. It supplies OpenTelemetry Operator chart 0.123.0, operator image 0.159.0,
and native CRDs. NKTP disables its default Collector and general Collector RBAC.
Settings pass through under `opentelemetry-kube-stack` using upstream values.

Helm installs missing OpenTelemetry CRDs (OpenTelemetryCollector, Instrumentation,
TargetAllocator, OpAMPBridge) and Prometheus CRDs (ServiceMonitor, PodMonitor, Probe,
ScrapeConfig) before the chart's resources.

### Kubernetes component metrics

NKTP enables the kube-stack ServiceMonitors for kube-apiserver,
kube-controller-manager, kube-scheduler, etcd, CoreDNS, and kube-proxy. They are
labeled `nktp.nvidia.io/large-target: "true"` and selected by the large-target
metrics Target Allocator. Component availability, labels, ports, and network
exposure vary between Kubernetes distributions, so a rendered ServiceMonitor
does not by itself guarantee a reachable target. The large-target Collector's
service account has read-only authorization for the `/metrics` non-resource URL.

The ServiceMonitors from opentelemetry-kube-stack 0.22.0 reference bearer-token
and, for secure endpoints, CA files in the Collector container. The large-target
Target Allocator therefore sets `denyFSAccessThroughSMs: false`. Setting it to
`true` causes these ServiceMonitor endpoints to be dropped, as described in
[opentelemetry-helm-charts issue #2408](https://github.com/open-telemetry/opentelemetry-helm-charts/issues/2408).

The minimum supported API version for `OpenTelemetryCollector` resources is `opentelemetry.io/v1beta1`.

Use these independent settings when another mechanism manages the Operator or CRDs.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    enabled: false
  crds:
    installOtel: false
    installPrometheus: false
```

NKTP uses an existing cert-manager installation to issue and renew the
Operator's admission webhook certificate by default. The upstream Operator
chart documents the available approaches under
[TLS Certificate Requirement](https://github.com/open-telemetry/opentelemetry-helm-charts/blob/opentelemetry-operator-0.123.0/charts/opentelemetry-operator/README.md#tls-certificate-requirement).

As an alternative, disable cert-manager integration and let Helm generate a
self-signed certificate. This configuration reuses that certificate on
upgrades; it expires after 365 days by default and is not renewed automatically.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: false
      autoGenerateCert:
        enabled: true
        recreate: false
```

NKTP also explicitly enables cert-manager-backed mTLS between each Collector
and its Target Allocator. The Operator provisions a CA plus server and client
certificates for each pair, as described in
[With cert-manager (default)](https://github.com/open-telemetry/opentelemetry-operator/blob/main/docs/target-allocator/README.md#with-cert-manager-default).

As an alternative, set `useCertManager: false` and reference certificates that
already exist in the release namespace. Apply the same `mtls` block under each
enabled allocator at `watchdog.kubernetes.targetAllocator`,
`daemonset.kubernetes.targetAllocator`, and
`large_target_metrics.kubernetes.targetAllocator`. For example:

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      mtls:
        enabled: true
        useCertManager: false
        tls:
          certificateAuthorityCertificate:
            configMap:
              name: target-allocator-ca
          serverCertificate:
            certificateSecret:
              name: target-allocator-server
            keySecret:
              name: target-allocator-server
          clientCertificate:
            certificateSecret:
              name: target-allocator-client
            keySecret:
              name: target-allocator-client
```

See the Operator's
[user-provided certificate documentation](https://github.com/open-telemetry/opentelemetry-operator/blob/main/docs/target-allocator/README.md#without-cert-manager-user-provided-certificates)
for the accepted Secret and ConfigMap references, default keys, and certificate
rotation behavior.

Override the Operator image with
`opentelemetry-kube-stack.opentelemetry-operator.manager.image.tag`.

### Node exporter

NKTP installs [prometheus-node-exporter](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-node-exporter) by default
to collect host-level metrics from every node. It runs as a DaemonSet with host networking and binds port 9100 on each node,
and ships a ServiceMonitor so the exporter is discoverable as a scrape target.

Disable it, for example when the cluster already runs a node exporter:

```yaml
node-exporter:
  enabled: false
```

### Metrics server

[metrics-server](https://github.com/kubernetes-sigs/metrics-server) serves the Kubernetes
resource metrics API (`metrics.k8s.io`), which `kubectl top` and Horizontal Pod Autoscalers
read. It is what drives horizontal autoscaling of the three signal gateways, the
large-target metrics pool, and the watchdog: every workload with an `autoscaler` block
scales on CPU and memory utilization, which the HPA controller can only read from this API.

NKTP does **not** install it by default, because many clusters already get it from a
managed control plane or a cluster add-on, and a second copy conflicts over the
cluster-singleton `v1beta1.metrics.k8s.io` APIService. With no provider at all, those HPAs
cannot compute a recommendation and stop scaling, holding each workload at whatever
replica count it currently has. Enable it when nothing else supplies the API:

```yaml
metrics-server:
  enabled: true
```

The bundled chart is pinned to metrics-server chart 3.13.1, which installs metrics-server
0.8.1.

Settings pass through under `metrics-server` using the upstream chart's own values, none
of which NKTP overrides. See the [chart's values reference](https://github.com/kubernetes-sigs/metrics-server/blob/metrics-server-helm-chart-3.13.1/charts/metrics-server/README.md)
for what is configurable, including the TLS defaults for the APIService and for kubelet
certificate verification.

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install.
Prometheus is available on port 9090 of the `lgtm` Service, for example
`http://lgtm.nktp.svc:9090` when installed in namespace `nktp`. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

The default **NKTP Health** dashboard shows workload readiness, pipeline faults,
queues and telemetry flow. It includes component detail, diagnostic logs,
internal traces and links to the component dashboards. Workload readiness uses
kube-state-metrics through the metrics gateway. Collector and Target Allocator
self metrics use watchdog and do not require the metrics gateway. Target
Allocator counts stay separate for each replica. Disabling LGTM removes its dashboards. Target Allocator metrics
continue to flow to the backend configured in watchdog, which must support delta metrics.

KSM scrape panels show samples, sample-count growth and duration. Export and
queue admission delays use internal spans through Tempo's TraceQL metrics support.
Container resource tables show requests and limits for up to three
containers per component. The dashboard also
shows allocator allocation duration, KSM shard coverage and node-exporter diagnostics.

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
