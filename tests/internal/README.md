# Shared Test Framework

Reusable test infrastructure shared by NKTP test suites, including chart
installation and readiness under `framework/`, Kubernetes API and Helm helpers,
load generation, validation, diagnostics, and reporting.

## Goals

- Provide consistent setup, cleanup, failure handling, and result reporting.
- Keep reusable test behavior in a central module rather than duplicating it across suites or scripts.

## Constraints

- Packages here must not contain suite-specific scenarios or assertions.
- APIs should remain usable by packages throughout the `tests/` module.
- External resources must have explicit lifecycle and cleanup behavior.
