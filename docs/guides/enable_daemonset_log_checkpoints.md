# Enable log checkpoints for the node Collector

The node Collector's `filelog` and `journald` receivers start at the end of
each file and journal, and keep their read positions in memory. When a
Collector restarts or its Pod is replaced, for example during a chart upgrade,
lines written while it was down are skipped. This guide stores the read
positions in a directory on each node with the `file_storage` extension, which
the default Collector image includes, so the replacement Collector resumes
where the previous one stopped. This uses the receivers' own
[offset tracking](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/receiver/filelogreceiver#offset-tracking)
and
[cursor tracking](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/receiver/journaldreceiver#cursor-tracking).

The [values example](../examples/daemonset_log_checkpoints.values.yaml)
contains every setting below.

## Enable checkpoints

From the repository root, install or upgrade with the values example:

```console
helm upgrade --install nktp ./nktp --namespace <release-namespace> \
  --values docs/examples/daemonset_log_checkpoints.values.yaml
```

It mounts a host directory into the Collector:

```yaml
daemonset:
  kubernetes:
    volumes:
      # ...default volumes...
      - name: log-checkpoints
        hostPath:
          path: /var/lib/nktp/log-checkpoints
          type: DirectoryOrCreate
    volumeMounts:
      # ...default volume mounts...
      - name: log-checkpoints
        mountPath: /var/lib/otelcol/log-checkpoints
```

Volumes and mounts are lists, which Helm replaces, so the example restates the
chart's defaults from `nktp/values.yaml`. Merge the lists by hand when
combining it with another example, such as
[Enable host process metrics](enable_host_process_metrics.md).

It then adds the extension and sets `storage` on every log receiver:

```yaml
daemonset:
  otelcol:
    config:
      extensions:
        file_storage/log-checkpoints:
          directory: /var/lib/otelcol/log-checkpoints
          create_directory: true
          timeout: 1s
      receivers:
        filelog:
          storage: file_storage/log-checkpoints
        filelog/host-auth:
          storage: file_storage/log-checkpoints
        filelog/host-audit:
          storage: file_storage/log-checkpoints
        filelog/kube-api-audit:
          storage: file_storage/log-checkpoints
        filelog/targetallocator:
          storage: file_storage/log-checkpoints
        filelog/operator:
          storage: file_storage/log-checkpoints
        journald:
          storage: file_storage/log-checkpoints
      service:
        extensions: [health_check, file_storage/log-checkpoints]
```

`service.extensions` restates `health_check` for the same reason. Add `storage`
to any log receiver you add. Each receiver's checkpoints are stored under its
name, so renaming a receiver starts it again from `start_at`.

## What is kept

The host directory survives Collector restarts, Pod replacement, and node
reboots. `start_at` still applies to files and journals with no checkpoint,
such as new Pod log files.

Lines are still lost when the kubelet rotates a log file away before the
Collector reads it, or when the node itself is replaced.

## Security and cleanup

The node Collector runs as root, so it can write to the host directory. Keep
the path dedicated to NKTP. The directory stays on each node after NKTP is
uninstalled; remove it from the nodes when it is no longer needed.

Each node has one directory. Use a different `hostPath.path` for each NKTP
release when running more than one on the same nodes.

## Verify

List the checkpoint files on a node:

```console
kubectl debug node/<node-name> -it --image=docker.io/library/busybox:1.37 \
  -- ls -la /host/var/lib/nktp/log-checkpoints
```

There is one file per log receiver, named after the receiver.
