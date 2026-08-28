# Scale Tests

Scenario-driven tests of NKTP correctness, stability, and capacity across large real or simulated Kubernetes topologies and telemetry loads.

## Goals

- Identify operational limits, saturation behavior, and scale-dependent failures.
- Produce reproducible reports that relate outcomes to topology, load, and configuration.

## Constraints

- Scenarios must declare their topology, workload, duration, and resource assumptions.
- Runs must preserve diagnostics and clean up all managed resources.
- These tests are resource intensive and are not intended to be run as part of the default pull-request test path.
