#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Wait for Helm to apply the chart and for the optional Grafana UI to respond.
set -eu

last_status=''
spinner_pid=''
started_at=$(date +%s)

stop_spinner() {
    if [ -n "$spinner_pid" ]; then
        kill "$spinner_pid" 2>/dev/null || true
        wait "$spinner_pid" 2>/dev/null || true
        spinner_pid=''
    fi
}

status() {
    [ "$1" != "$last_status" ] || return 0
    stop_spinner
    if [ -t 1 ]; then
        (
            trap - INT TERM EXIT
            while :; do
                for frame in '|' '/' '-' '\'; do
                    elapsed=$(( $(date +%s) - started_at ))
                    printf '\r\033[K%s %s (%ss)' "$frame" "$1" "$elapsed"
                    sleep 0.2
                done
            done
        ) &
        spinner_pid=$!
    else
        printf '%s\n' "$1"
    fi
    last_status=$1
}

clear_status() {
    stop_spinner
    if [ -t 1 ] && [ -n "$last_status" ]; then
        printf '\r\033[K'
    fi
    last_status=''
}

trap clear_status EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
    clear_status
    echo "$1" >&2
    tail -n 20 .tilt.log >&2 || true
    exit 1
}

check_tilt() {
    kill -0 "$(cat .tilt.pid)" 2>/dev/null || fail "Tilt exited before startup completed."
}

status "Waiting for the Tilt API..."
attempt=0
until tilt get uiresource '(Tiltfile)' >/dev/null 2>&1; do
    check_tilt
    attempt=$((attempt + 1))
    [ "$attempt" -lt 60 ] || fail "Tilt API did not become available within 60 seconds."
    if [ $((attempt % 10)) -eq 0 ]; then
        status "Waiting for the Tilt API..."
    fi
    sleep 1
done

# Inspect LGTM only after the current Helm apply has completed.
for stage in tiltfile helm grafana; do
    deadline=$(( $(date +%s) + 600 ))
    while :; do
        check_tilt
        errors=$(tilt get uiresources -o 'jsonpath={range .items[*]}{.status.buildHistory[0].error}{"\n"}{end}')
        if [ -n "$(printf '%s' "$errors" | tr -d '\n')" ]; then
            clear_status
            printf '%s\n' "$errors" | sed '/^$/d' >&2
            fail "Tilt reported a build or configuration error."
        fi
        if [ "$stage" = tiltfile ]; then
            status "Waiting for Tilt configuration..."
            if tilt wait --for=condition=Ready 'uiresource/(Tiltfile)' --timeout=2s >/dev/null 2>&1; then
                break
            fi
        elif [ "$stage" = helm ]; then
            status "Waiting for Helm to apply NKTP..."
            if [ "$(tilt get uiresource nktp -o 'jsonpath={.status.updateStatus}')" = ok ]; then
                lgtm=$(kubectl --kubeconfig .kubeconfig --context kind-nktp -n nktp get deployment/lgtm --ignore-not-found --request-timeout=10s -o name) || fail "Could not check the LGTM Deployment."
                if [ -z "$lgtm" ]; then
                    clear_status
                    echo "NKTP deployed. LGTM is disabled. Follow workload startup in Tilt."
                    exit 0
                fi
                break
            fi
            sleep 2
        else
            status "Waiting for Grafana..."
            [ "$(tilt get uiresource grafana -o 'jsonpath={.status.runtimeStatus}')" != error ] || fail "Grafana port-forward exited. Check the Tilt logs."
            if grep -q '^Forwarding from 127.0.0.1:3000' .grafana-forward.log 2>/dev/null && curl --fail --silent --max-time 2 http://127.0.0.1:3000/api/health >/dev/null; then
                clear_status
                echo "Grafana available at http://127.0.0.1:3000. Other workloads may still be starting."
                exit 0
            fi
            sleep 2
        fi
        [ "$(date +%s)" -lt "$deadline" ] || fail "Timed out waiting for $stage. Tilt remains running for inspection."
    done
done
