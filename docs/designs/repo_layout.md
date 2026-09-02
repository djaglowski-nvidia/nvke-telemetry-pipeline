# Proposed Repository Layout

```text
nktp/
├── Chart.yaml
├── values.yaml
├── README.md
├── templates/
└── tests/                          # Self-contained unit test module
    ├── go.mod
    ├── go.sum
    ├── pass/                       # Scenarios that must succeed
    └── fail/                       # Scenarios that must be rejected

occt/                               # Optional: if owned by this repository
├── Chart.yaml
├── README.md
├── templates/
└── tests/                          # Self-contained unit test module
    ├── go.mod
    ├── go.sum
    ├── pass/                       # Scenarios that must succeed
    └── fail/                       # Scenarios that must be rejected

blueprints/                         # Optional: for blueprints owned by this repo
├── README.md
└── <blueprint-name>/
    ├── Chart.yaml
    ├── values.yaml
    ├── README.md
    └── templates/

tests/
├── go.mod
├── go.sum
├── internal/
├── integration/
├── e2e/
├── scale/
├── perf/
└── testdata/

docs/
├── designs/                       # Architecture, design proposals, and decisions
├── concepts/                      # Core NKTP terminology and mental models
├── getting-started/               # Installation and first-deployment guidance
├── how-to/                        # Task-oriented procedures for common workflows
├── service-owners/                # Guidance for workload teams consuming NKTP
├── operations/                    # Day-two administration and troubleshooting
├── reference/                     # Detailed configuration, schemas, and interfaces
└── testing/                       # Test strategy, execution, and scenario guidance

dev/                               # Local development tooling and scripts

.github/{actions,workflows,ISSUE_TEMPLATE}/
Taskfile.yml
README.md
CONTRIBUTING.md
GOVERNANCE.md
MAINTAINERS.md
SECURITY.md
CODE_OF_CONDUCT.md
LICENSE
NOTICE
RELEASE.md
```
