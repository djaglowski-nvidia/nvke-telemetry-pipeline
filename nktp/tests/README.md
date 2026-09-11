# NKTP Chart Unit Tests

Tests use [helm-unittest](https://github.com/helm-unittest/helm-unittest):

```sh
helm plugin install https://github.com/helm-unittest/helm-unittest.git --version 1.1.2
task test   # from the repository root
```

The default output of NKTP's own templates is always covered by a snapshot
(`default_snapshot_test.yaml`, goldens in `__snapshot__/`). After an
intentional change to default output, regenerate and review the diff:

```sh
task test -- -u
git diff
```

Bundled charts and other configurations use targeted assertions. Add snapshots only for
output that is worth pinning in full.

The CRD suite uses `includeCrds: true` to check native CRDs and their installation flags.
