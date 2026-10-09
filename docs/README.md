# Documentation

The [chart README](../nktp/README.md) summarizes the chart: prerequisites,
installation, components, and the configuration model. The pages here cover
each part in depth.

## Run NKTP

- [Getting started](getting-started.md)
- [Topology](architecture/topology.md)
- [Backpressure](architecture/backpressure.md)
- [Self-telemetry sidecars](architecture/self_telemetry.md)
- [Size resources](guides/size_resources.md)
- [Configure cert-manager](guides/configure_cert_manager.md)
- [Secure gateway export](guides/secure_gateway_export.md)
- [Configure kubelet metrics](guides/configure_kubelet_metrics.md)
- [Enable host process metrics](guides/enable_host_process_metrics.md)
- [Enable log checkpoints for the node Collector](guides/enable_daemonset_log_checkpoints.md)

## Send telemetry to NKTP

- [Send telemetry to NKTP](guides/send_telemetry.md)

## Components

Each page covers one top-level key in `nktp/values.yaml`.

- [Node collector](components/node_collector.md) (`daemonset`)
- [Gateways](components/gateways.md) (`gateway_logs`, `gateway_metrics`, `gateway_traces`)
- [Large-target metrics](components/large_target_metrics.md) (`large_target_metrics`)
- [Cluster events](components/cluster_events.md) (`cluster_events`)
- [Watchdog](components/watchdog.md) (`watchdog`)
- [kube-state-metrics](components/kube_state_metrics.md) (`kube-state-metrics`)
- [otel-lgtm](components/otel_lgtm.md) (`otel-lgtm`)
- [Managed dependencies](components/dependencies.md) (`opentelemetry-kube-stack`, `node-exporter`, `metrics-server`)

## Develop NKTP

- [Design principles](design_principles.md)
- [Contributing](../CONTRIBUTING.md)
- [Local development loop](../dev/README.md)
- [Chart unit tests](../nktp/tests/README.md)
- [End-to-end tests](../tests/e2e/README.md)
- [Dashboards](../nktp/dashboards/README.md)
- [OCCT](../occt/README.md)
- [Releasing](../RELEASING.md)

## Design documents

- [Required target allocation](designs/target_allocation.md)
- [kube-state-metrics](designs/kube_state_metrics.md)
