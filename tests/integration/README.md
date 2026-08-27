# Integration Tests

Tests of interactions between NKTP components, local processes, or test services without provisioning a Kubernetes cluster.

## Goals

- Validate component boundaries and assembled configurations beyond isolated unit behavior.
- Detect compatibility and lifecycle failures while remaining faster and simpler than end-to-end tests.

## Constraints

- May use ephemeral resources on the local machine, including loopback services and fake or local API servers.
- Must not require Kind, an existing Kubernetes cluster, or access to remote services.
- Must create, isolate, and clean up every resource they use.
