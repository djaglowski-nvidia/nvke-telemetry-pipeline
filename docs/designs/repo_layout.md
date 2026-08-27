# Proposed Repository Layout

```text
nktp/
├── Chart.yaml                      # Declares the external otelgen dependency
├── values.yaml
├── README.md
├── templates/

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
├── unit/
│   ├── nktp/
│   └── blueprints/                # Present only for locally owned blueprints
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
