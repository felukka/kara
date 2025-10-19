# Operator configuration

The operator is a single manager container. Everything about *your pools* is configured in `RunnerPool` resources; this page covers the manager itself.

## Flags

The bundled manifests (Kustomize and Helm) pass a sensible set of flags. The full list:

| Flag | Default | Description |
|---|---|---|
| `--health-probe-bind-address` | `:8081` | Address for the `/healthz` and `/readyz` endpoints. |
| `--metrics-bind-address` | `0` | Address for metrics. `:8443` for HTTPS, `:8080` for HTTP, `0` to disable. |
| `--metrics-secure` | `true` | Serve metrics over HTTPS. |
| `--metrics-cert-path` / `--metrics-cert-name` / `--metrics-cert-key` | `""` / `tls.crt` / `tls.key` | Where to read the metrics certificate from. When the path is empty and metrics are secure, the manager generates a self-signed certificate. |
| `--leader-elect` | `false` | Enable leader election (the bundled manifests enable it). |
| `--webhook-cert-path` | `""` | Directory containing the webhook certificate. |
| `--webhook-cert-name` / `--webhook-cert-key` | `tls.crt` / `tls.key` | Webhook certificate file names. |
| `--enable-http2` | `false` | Enable HTTP/2 on the metrics and webhook servers. |
| `-zap-log-level` | `info` | Log verbosity (`info`, `debug`, ...). |
| `-zap-encoder` | `console` | Log encoding (`console` or `json`). |

The Helm chart maps most of these to values — see [Helm chart](../helm/kara.md).

## Environment

| Variable | Effect |
|---|---|
| `ENABLE_WEBHOOKS` | Set to `false` to skip registering the webhooks (the manifests leave them on). |

## Reconciliation model

There are no background poller goroutines. Each `RunnerPool` is reconciled on its own cadence:

1. On every reconcile the operator fetches the pool's **queued job requests** and **agents** once.
2. It reconciles agents (scale up / scale down / idle cleanup / error cleanup) and updates the status.
3. It requeues after `spec.pollIntervalSeconds` (minimum 5 s), so the effective polling rate per pool is controlled by the resource, not by the operator.

This means 100 pools cost 100 cheap, independent requeue timers — there is no shared polling loop to tune.

## Metrics

The metrics endpoint is HTTPS by default and protected by Kubernetes RBAC (a `TokenReview` / `SubjectAccessReview`-based auth handler). The bundled manifests create:

- a `metrics-reader` ClusterRole (read `/metrics`), and
- the `metrics-auth-role` that lets the manager authenticate the caller.

Scrape it from Prometheus by binding `metrics-reader` to your Prometheus service account, e.g.:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: prometheus-metrics-reader
  namespace: kara-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kara-metrics-reader
subjects:
- kind: ServiceAccount
  name: prometheus
  namespace: monitoring
```

## Observability

- **Health** — `/healthz` (liveness) and `/readyz` (readiness) on the probe address.
- **Status** — per-pool conditions and `lastError` on the `RunnerPool` (see [RunnerPool → Status](../resources/runnerpool.md#status)).
- **Events** — the operator records Kubernetes events on the `RunnerPool` for scale up/down and errors, so `kubectl describe runnerpool <name>` is a good first stop.
