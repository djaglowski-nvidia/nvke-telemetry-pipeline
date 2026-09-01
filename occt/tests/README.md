# OCCT Chart Unit Tests

`occt` is a library chart: it declares no templates of its own and renders
nothing. `test-chart` acts as the driver that calls `occt.transpile` under the hood to render templates for the test suite.

Each directory under `pass/` is a scenario. The test chart is installed with the
scenario's `values.yaml` in dry-run mode and the resulting manifest must match
its `output.yaml` exactly. Run the tests:

```sh
go test ./...
```

After an intentional template or values change, regenerate the golden files
and review the diff:

```sh
go test -update ./...
git diff
```

To add a scenario, create `pass/<name>/values.yaml` and run `go test -update ./...`.
