# NKTP Chart Unit Tests

Each directory under `pass/` is a scenario. The chart is rendered with the
scenario's `values.yaml` and must match its `output.yaml` exactly.

Run the tests:

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
