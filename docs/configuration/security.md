# Security

## PAT handling

- The PAT lives in a Kubernetes **Secret** (`patSecretName` / `patSecretKey`). It is never written to the `RunnerPool` resource, logs, or the agent pod spec as a literal — the agent pod references the Secret through an `env.valueFrom.secretKeyRef`.
- The operator only needs *read* access to that Secret; RBAC grants `get`/`list`/`watch` on Secrets, scoped by the namespace the manager runs in.
- The PAT token should be scoped as tightly as possible:
  - **Agent pools**: Read, Manage
  - **Build**: Read, Invoke
  - Scope to the project(s) you care about if you do not need all projects.

> !!! tip
> Rotate the PAT by editing the Secret in place; the running operator picks up the new token on the next poll (each reconcile re-reads the Secret).

## RBAC

The operator Service account is bound to:

| Role | Scope | Grants |
|---|---|---|
| `manager-role` (ClusterRole) | cluster | Pods and PVCs (full CRUD), Secrets (read), `runnerpools` + `finalizers` + `status` (full), StorageClasses (read) |
| `leader-election-role` (Role) | operator namespace | ConfigMaps, Leases, Events |
| `metrics-auth-role` (ClusterRole) | cluster | Create `TokenReview` / `SubjectAccessReview` (used to authenticate metrics callers) |

There are also `runnerpool-admin` / `runnerpool-editor` / `runnerpool-viewer` ClusterRoles for giving *users* access to the resource.

## Webhooks and TLS

- The mutating and validating webhooks are served over TLS on port `9443` through the `webhook-service` Service (port 443).
- The serving certificate is issued by **cert-manager** (self-signed `Issuer` → `Certificate`). The manager mounts it from the resulting Secret.
- The webhook configurations carry the `cert-manager.io/inject-ca-from` annotation, so cert-manager injects the CA bundle into the `caBundle` field automatically — no manual `kubectl patch` step.
- `failurePolicy: Fail` means a webhook outage blocks `RunnerPool` create/update. If you need the API to stay writable when the operator is down, flip both webhooks to `failurePolicy: Ignore`.

## Metrics

- Served over **HTTPS** (`--metrics-secure` defaults to `true`) on `:8443`.
- With no `--metrics-cert-path` set, the manager generates a self-signed certificate at startup.
- Access is gated by **Kubernetes RBAC**: the endpoint authenticates the caller's token (via `TokenReview`) and authorizes it (via `SubjectAccessReview`) against the `metrics-reader` ClusterRole.
- The `allow-metrics-traffic` NetworkPolicy (in `config/network-policy`) restricts who can reach the metrics port — apply it if you enforce NetworkPolicy in the cluster.

## Agent pods

- Agent containers run as a **non-root user** (`runAsUser` / `runAsGroup`, default `1001`) by default.
- When you enable PVC-backed [storage](../resources/runnerpool.md#storage), an **init container** runs as root to `chown` the volumes to the agent user, then the agent itself runs unprivileged.
- Set `securityContext.privileged: true` on the RunnerPool only if your workloads genuinely need it (e.g. Docker-in-Docker). KARA will not enable it for you.
- `certTrustStore` Secrets are mounted read-only at `/etc/ssl/certs/<secretName>.crt` so the agent trusts your internal CAs.

## Network policies

`config/network-policy/` ships two policies (not applied by the default overlay):

- `allow-metrics-traffic` — only the Prometheus namespace may reach the metrics port.
- `allow-webhook-traffic` — only the API server may reach the webhook port.

Apply them alongside the default overlay if your cluster enforces NetworkPolicy:

```bash
kubectl apply -k config/network-policy
```

## Least privilege checklist

1. Scope the PAT to the project and the two permission sets above.
2. Keep `failurePolicy: Fail` unless you have a reason not to.
3. Bind `metrics-reader` only to the Prometheus service account.
4. Apply the network policies.
5. Store the PAT Secret in the operator namespace and keep its RBAC to `get`.
6. Avoid `privileged: true` on RunnerPools.
