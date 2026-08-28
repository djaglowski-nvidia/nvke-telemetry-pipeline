# Unit Tests

Fast, hermetic tests of chart rendering, configuration, schemas, and other isolated logic using only local fixtures and in-process dependencies.

## Goals

- Catch invalid manifests, configuration errors, and edge cases quickly.
- Produce deterministic results suitable for every pull request.

## Constraints

- Must not require a Kubernetes cluster, network access, or external services.
- Must not depend on shared mutable state or test execution order.
- Tests requiring local processes or multiple integrated components belong in `tests/integration/`.
