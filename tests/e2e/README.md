# End-to-End Tests

Tests of user-visible NKTP behavior after deploying the real chart to a Kind or explicitly provided Kubernetes cluster.

## Goals

- Validate installation, upgrades, removal, topology, dependencies, and Kubernetes API behavior.
- Verify complete telemetry paths where a scenario requires them.

## Constraints

- Must use isolated test resources and perform reliable cleanup.
- Must collect sufficient diagnostics to explain deployment and assertion failures.
- Scale and performance characterization belong in their dedicated suites.
