# NKTP Chart Unit Tests

Tests use [helm-unittest](https://github.com/helm-unittest/helm-unittest):

```sh
helm plugin install https://github.com/helm-unittest/helm-unittest.git --version 1.1.2
helm unittest nktp   # from the repository root
```

The full default configuration is always covered by a snapshot
(`default_snapshot_test.yaml`, goldens in `__snapshot__/`). After an
intentional change to default output, regenerate and review the diff:

```sh
helm unittest -u nktp
git diff
```

Everything else is covered by targeted assertions. Add snapshots only for
output that is worth pinning in full.
