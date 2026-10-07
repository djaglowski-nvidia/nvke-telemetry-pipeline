# Gateways

NKTP runs one gateway per signal. Each gateway accepts OTLP over gRPC and HTTP
at its Service in the Helm release namespace, forwards what it receives to LGTM
by default, and sends its own metrics, logs and traces through its
[sidecar](../architecture/self_telemetry.md) to watchdog.

| Gateway | Values key | gRPC | HTTP |
| --- | --- | --- | --- |
| Logs | `gateway_logs` | `nktp-gateway-logs-collector:4317` | `https://nktp-gateway-logs-collector:4318` |
| Metrics | `gateway_metrics` | `nktp-gateway-metrics-collector:4317` | `https://nktp-gateway-metrics-collector:4318` |
| Traces | `gateway_traces` | `nktp-gateway-traces-collector:4317` | `https://nktp-gateway-traces-collector:4318` |

## Collector connections

Node Collectors send logs, metrics and traces to their gateways over OTLP/gRPC
with mutual TLS.
The large-target pool sends metrics over gRPC, and the cluster-events reader
sends logs over gRPC. All three gateways send GOAWAY at a connection age of five
minutes, with +/-10% jitter, so clients reconnect through the Service and can
reach newly added replicas. In-flight exports get another 30 seconds to finish
before forced closure. See
[gRPC connection management](https://github.com/grpc/proposal/blob/master/A9-server-side-conn-mgt.md).

Five minutes limits routine connection churn while periodically redistributing
telemetry. Tune `max_connection_age` under
`<gateway>.otelcol.config.receivers.otlp.protocols.grpc.keepalive.server_parameters`
based on gateway headroom and acceptable delay after scale-up. Lower it for
faster relief; raise it to reduce TCP setup and TLS handshakes.
Reconnects can select the same gateway, so redistribution can take multiple
rotations. Increase `max_connection_age_grace` if in-flight exports need longer
to finish. HTTP receivers remain available, but their connections do not use
these gRPC age settings.

Exporter overrides use `daemonset.otelcol.config.exporters.otlp/logs`,
`otlp/metrics` and `otlp/traces`, and `large_target_metrics.otelcol.config.exporters.otlp`
and `cluster_events.otelcol.config.exporters.otlp`.

Use `<gateway>.otelcol.config` for Collector configuration and
`<gateway>.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `<gateway>.kubernetes.env`.

To export to a production backend, see
[Set a backend](../getting-started.md#set-a-backend) and
[Secure gateway export](../guides/secure_gateway_export.md).
