# Configure cert-manager

NKTP depends on cert-manager to reap the benefits of following features. These features are not explicitly required, but are enabled by default and represent the best-practices for deploying NKTP.

## Installing NKTP without cert-manager

cert-manager has three uses in NKTP. Replace each to install without it:

```yaml
tls:
  internal:
    enabled: false
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: false
      autoGenerateCert:
        enabled: true
        recreate: false
```

## Usages

### Target Allocators

NKTP explicitly enables cert-manager-backed mTLS between each Collector
and its Target Allocator. The Operator provisions a CA plus server and client
certificates for each pair, as described in
[With cert-manager (default)](https://github.com/open-telemetry/opentelemetry-operator/blob/main/docs/target-allocator/README.md#with-cert-manager-default).

As an alternative, set `useCertManager: false` and reference certificates that
already exist in the release namespace. Apply the same `mtls` block under each
enabled allocator at `watchdog.kubernetes.targetAllocator`,
`daemonset.kubernetes.targetAllocator`, and
`large_target_metrics.kubernetes.targetAllocator`. For example:

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      mtls:
        enabled: true
        useCertManager: false
        tls:
          certificateAuthorityCertificate:
            configMap:
              name: target-allocator-ca
          serverCertificate:
            certificateSecret:
              name: target-allocator-server
            keySecret:
              name: target-allocator-server
          clientCertificate:
            certificateSecret:
              name: target-allocator-client
            keySecret:
              name: target-allocator-client
```

See the Operator's
[user-provided certificate documentation](https://github.com/open-telemetry/opentelemetry-operator/blob/main/docs/target-allocator/README.md#without-cert-manager-user-provided-certificates)
for the accepted Secret and ConfigMap references, default keys, and certificate
rotation behavior.

### Admission Webhook

NKTP uses an existing cert-manager installation to issue and renew the
Operator's admission webhook certificate by default. The upstream Operator
chart documents the available approaches under
[TLS Certificate Requirement](https://github.com/open-telemetry/opentelemetry-helm-charts/blob/opentelemetry-operator-0.123.0/charts/opentelemetry-operator/README.md#tls-certificate-requirement).

As an alternative, disable cert-manager integration and let Helm generate a
self-signed certificate. This configuration reuses that certificate on
upgrades; it expires after 365 days by default and is not renewed automatically.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: false
      autoGenerateCert:
        enabled: true
        recreate: false
```

### Internal mTLS

By default, NKTP uses cert-manager to create a self-signed CA, issuer, and certificates for each collector 
used for collector-to-gateway traffic encryption. Setting `certManager.internalTLS.issuerRef` allows you to specify
an existing `Issuer` or `ClusterIssuer`.

```bash
certManager:
  internalTLS:
    enabled: true
    issuerRef: {}
```

The cluster event reader, DaemonSet, and large-target metrics collectors each use a client certificate to send
telemetry to the gateways over mTLS. Each gateway uses a server certificate.

Traffic to watchdog is not covered by this setting. Collector and Target Allocator self-telemetry, and the
DaemonSet's Target Allocator and Operator logs, are sent to watchdog over plain HTTP.

```bash
kubectl get issuers,certs -n nktp
NAME                                                     READY   AGE
issuer.cert-manager.io/nktp-internal-ca-issuer           True    48s
issuer.cert-manager.io/nktp-internal-selfsigned-issuer   True    48s

NAME                                                           READY   SECRET                                          AGE
certificate.cert-manager.io/nktp-cluster-events-client         True    nktp-cluster-events-client-tls                  48s
certificate.cert-manager.io/nktp-daemonset-client              True    nktp-daemonset-client-tls                       48s
certificate.cert-manager.io/nktp-gateway-logs-server           True    nktp-gateway-logs-server-tls                    48s
certificate.cert-manager.io/nktp-gateway-metrics-server        True    nktp-gateway-metrics-server-tls                 48s
certificate.cert-manager.io/nktp-gateway-traces-server         True    nktp-gateway-traces-server-tls                  48s
certificate.cert-manager.io/nktp-internal-ca                   True    nktp-internal-ca                                48s
certificate.cert-manager.io/nktp-large-target-metrics-client   True    nktp-large-target-metrics-client-tls            48s
```

**NOTE:** Gateway->Backend encryption requires a manually configured client certificate for the gateways and is not part of this setting.

Disabling `certManager.internlaTLS` requires you to manually reconfigure the collector configs to use `http` endpoints and not include `tls` blocks.

