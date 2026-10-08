#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Run a pull-through cache for each upstream registry, and point the Kind node
# at them. The caches run outside the cluster and keep their volumes, so images
# pulled once are served locally after dev:down and dev:up.
set -euo pipefail

node=nktp-control-plane
image=registry:3

for mirror in \
    docker.io=https://registry-1.docker.io \
    ghcr.io=https://ghcr.io \
    quay.io=https://quay.io \
    registry.k8s.io=https://registry.k8s.io; do
    host=${mirror%%=*}
    upstream=${mirror#*=}
    name="nktp-mirror-${host//./-}"

    if ! docker inspect "$name" >/dev/null 2>&1; then
        echo "Starting registry mirror for $host..."
        docker run -d --restart=always --name "$name" \
            -v "$name:/var/lib/registry" \
            -e REGISTRY_PROXY_REMOTEURL="$upstream" \
            "$image" >/dev/null
    elif [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ]; then
        docker start "$name" >/dev/null
    fi

    # The Kind node resolves the mirror by name on the kind network.
    if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "$name")" = null ]; then
        docker network connect kind "$name"
    fi

    docker exec "$node" mkdir -p "/etc/containerd/certs.d/$host"
    printf 'server = "%s"\n\n[host."http://%s:5000"]\n  capabilities = ["pull", "resolve"]\n' \
        "$upstream" "$name" |
        docker exec -i "$node" cp /dev/stdin "/etc/containerd/certs.d/$host/hosts.toml"
done
