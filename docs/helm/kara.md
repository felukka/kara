# Helm chart: kara

The chart lives in [`chart/kara`](https://github.com/mahmoudk1000/kara/tree/go/chart/kara) and installs the CRD, RBAC, the operator Deployment, the metrics and webhook Services, and the cert-manager resources that power the webhooks.

```bash
helm install kara chart/kara \
  --namespace kara-system --create-namespace \
  --set image.repository=<some-registry>/kara \
  --set image.tag=tag
```

## Values

### Core

| Value | Default | Description |
|---|---|---|
| `createNamespace` | `true` | Create the release namespace. |
| `replicaCount` | `1` | Operator replicas. Keep at 1 with leader election on. |
| `nameOverride` / `fullnameOverride` | `""` | Override the generated names. |
| `image.repository` | `ghcr.io/mahmoudk1000/kara/operator` | Operator image. |
| `image.tag` | chart `appVersion` | Operator image tag. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Secrets for private registries. |

### Service account

| Value | Default | Description |
|---|---|---|
| `serviceAccount.create` | `true` | Create the Service account. |
| `serviceAccount.name` | generated | Name to use (or reference). |
| `serviceAccount.annotations` | `{}` | Extra annotations. |

### Scheduling and pod

| Value | Default | Description |
|---|---|---|
| `podAnnotations` / `podLabels` | `{}` | Extra pod metadata. |
| `podSecurityContext` | `runAsNonRoot: true`, seccomp `RuntimeDefault` | Pod-level security context. |
| `securityContext` | drop ALL caps, `readOnlyRootFilesystem: true` | Container security context. |
| `nodeSelector` / `tolerations` / `affinity` | empty | Standard scheduling knobs. |
| `resources` | 100m/128Mi requests, 500m/256Mi limits | Operator container resources. |
| `extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | Escapes for custom env and volumes. |

### Features

| Value | Default | Description |
|---|---|---|
| `leaderElection.enabled` | `true` | Adds `--leader-elect`. |
| `webhook.enabled` | `true` | Installs the webhook Service and both webhook configurations. |
| `webhook.port` | `9443` | Port the manager serves the webhook on. |
| `certManager.enabled` | `true` | Installs the Issuer/Certificate. Keep in sync with `webhook.enabled`. |
| `metrics.enabled` | `true` | Installs the metrics Service and auth RBAC; binds `--metrics-bind-address`. |
| `metrics.bindAddress` | `:8443` | Metrics bind address (HTTPS). |
| `healthProbe.bindAddress` | `:8081` | Health/ready probe address. |

## Notes

- The webhook serving certificate is issued by cert-manager; its CA is injected into the webhook configurations via the `cert-manager.io/inject-ca-from` annotation. Set both `webhook.enabled` and `certManager.enabled` to `false` on clusters without cert-manager (webhooks then stay off).
- The CRD is in the chart's `crds/` directory: Helm installs it before the templates and does **not** upgrade it on `helm upgrade` (the standard Helm behavior).
- `kubectl get runnerpools -w` works immediately after install; agent pods appear when you create a `RunnerPool` (see [Quickstart](../getting-started/quickstart.md)).
