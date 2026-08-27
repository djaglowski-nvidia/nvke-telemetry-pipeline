# Proposed Repository Layout

```text
nktp/
├── Chart.yaml
├── values.yaml
├── README.md
└── templates/

otelgen/                            # Optional: if owned by this repository
├── Chart.yaml
├── README.md
└── templates/

modules/                            # Optional: if owned by this repository
├── README.md
└── <module-name>/
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
│   ├── otelgen/                   # Present only if otelgen is owned here
│   └── modules/                   # Present only for locally owned modules
├── functional/
├── scale/
├── perf/
└── testdata/

docs/
├── designs/
│   └── repo_layout.md
├── concepts/
├── getting-started/
├── how-to/
├── service-owners/
├── operations/
├── reference/
└── testing/

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
