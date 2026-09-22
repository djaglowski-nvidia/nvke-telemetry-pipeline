# Local Development

Requires a Docker-compatible container runtime ([Docker Desktop](https://docs.docker.com/get-docker/), [Colima](https://github.com/abiosoft/colima), ...), [Task](https://taskfile.dev/installation/), [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation), [kubectl](https://kubernetes.io/docs/tasks/tools/), [Helm](https://helm.sh/docs/intro/install/), and [Tilt](https://docs.tilt.dev/install.html).
```sh
task dev:up       # Kind cluster + NKTP chart + Tilt dev loop
task dev:down     # remove the workloads and delete the cluster
```

The dev loop runs in the background (logs in `dev/.tilt.log`). Edits to the chart or the active config redeploy automatically.

The tooling never touches kubectl contexts or clusters other than its own. To
point kubectl at the dev cluster, run the `export KUBECONFIG=...` line that
`task dev:up` prints.

## Configs

The dev loop applies [Kind host paths](values.yaml) to collect
runtime journals from `/run/log/journal`. A dev config is a directory
under `dev/configs/` holding a `values.yaml` that Tilt layers on top, the same
as passing `-f` to helm. Use one to try a different configuration locally,
such as pointing a workload at another backend, without touching
`nktp/values.yaml`. See `dev/configs/contrib/`.

```sh
task dev:up -- <name>             # start or reconfigure with dev/configs/<name>/
task dev:up                       # chart defaults with Kind host paths
```

Tilt watches the active config, so edits to it also redeploy the chart.
