# Local Development

Requires a Docker-compatible container runtime ([Docker Desktop](https://docs.docker.com/get-docker/), [Colima](https://github.com/abiosoft/colima), ...), [Task](https://taskfile.dev/installation/), `curl`, and `tar`.
`task dev:up` downloads pinned versions of Kind, kubectl, Helm, and Tilt into
`dev/.bin` and uses those instead of any installed copies. The versions are set
in [install-tools.sh](install-tools.sh).

```sh
task dev:up       # Kind cluster + cert-manager + NKTP chart + Tilt dev loop
task dev:down     # remove the workloads and delete the cluster
```

The dev cluster includes cert-manager and its CRDs. The dev loop runs in the
background (logs in `dev/.tilt.log`). Edits to the chart or the active config
redeploy automatically.
`dev:up` returns when Grafana is available, or after Helm applies the chart when
LGTM is disabled. Other workloads may still be starting. Follow their status in
Tilt.

Images are cached by a pull-through registry mirror for each upstream registry
(Docker Hub, ghcr.io, quay.io, and registry.k8s.io). The mirrors run as Docker
containers named `nktp-mirror-*`, outside the cluster, with their own volumes.
They keep running after `dev:down`, so an image is downloaded only once and later
clusters pull it locally. To clear the cache, remove those containers and
volumes. See [registry-mirrors.sh](registry-mirrors.sh).

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
