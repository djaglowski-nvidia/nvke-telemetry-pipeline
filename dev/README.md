# Local Development

Requires a Docker-compatible container runtime ([Docker Desktop](https://docs.docker.com/get-docker/), [Colima](https://github.com/abiosoft/colima), ...), [Task](https://taskfile.dev/installation/), [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation), [kubectl](https://kubernetes.io/docs/tasks/tools/), [Helm](https://helm.sh/docs/intro/install/), and [Tilt](https://docs.tilt.dev/install.html).
```sh
task dev:up       # Kind cluster + cert-manager + NKTP chart + Tilt dev loop
task dev:down     # remove the workloads and delete the cluster
```

The dev cluster includes cert-manager and its CRDs. The dev loop runs in the
background (logs in `dev/.tilt.log`). Edits to the chart or the active config
redeploy automatically.
`dev:up` returns when Grafana is available, or after Helm applies the chart when
LGTM is disabled. Other workloads may still be starting. Follow their status in
Tilt. Images for the node's platform are cached between cluster recreations and
restored with `kind load image-archive`. Cache preparation changes multi-platform
index digests while preserving native image contents and references. Cache restore
failures do not block startup.

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
