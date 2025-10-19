# RunnerPool

`RunnerPool` is the single KARA resource. It describes one Azure DevOps agent pool and how KARA should scale it.

```yaml
apiVersion: kara.felukka.org/v1
kind: RunnerPool
metadata:
  name: default
  namespace: ci
spec:
  azUrl: "https://dev.azure.com/<org>"
  pool: "Default"
  patSecretName: "kara-pat"
  image: "ghcr.io/mahmoudk1000/kara/agent:latest"
```

All resources are in the API group `kara.felukka.org/v1` and are **namespaced**. The operator, its Service account and the agent pods live in the same namespace; the PAT Secret must be in the RunnerPool's namespace.

## Spec

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `azUrl` | string | yes | — | Azure DevOps organization URL. Cloud: `https://dev.azure.com/<org>`. Server: `https://<server>/<collection>`. Must be HTTP(S). |
| `pool` | string | no | `default` | Name of the Azure DevOps agent pool to manage. |
| `patSecretName` | string | yes | — | Name of the Secret in this namespace that holds the PAT. |
| `patSecretKey` | string | no | `token` | Key inside `patSecretName` that holds the token. |
| `image` | string | yes* | `ghcr.io/mahmoudk1000/kara/agent:latest` | Image for agent pods. No spaces. (*defaulted by the webhook when empty.) |
| `imagePullPolicy` | string | no | `IfNotPresent` | `Always`, `Never` or `IfNotPresent`. |
| `maxAgents` | int | no | `5` | Hard cap on KARA-managed agents. Minimum 1. |
| `minAgents` | int | no | `0` | Agents always kept warm. Minimum 0, must be `<= maxAgents`. |
| `ttlIdleSeconds` | int | no | `300` | How long an idle (or just-completed) agent is kept before it is unregistered and deleted. `0` = one-shot mode. |
| `pollIntervalSeconds` | int | no | `5` | How often KARA polls the pool queue. Minimum 5. |
| `capabilityAware` | bool | no | `false` | Scale per demanded capability instead of by raw queue depth. |
| `capabilities` | map[string]string | no | — | Default capabilities registered on the agent (e.g. `{"os": "linux"}`), passed as `Agent.<name>` env vars. Ignored in capability-aware mode. |
| `capabilityImages` | map[string]string | no | — | Capability name → agent image, used in capability-aware mode. |
| `initContainerSpec.image` | string | no | — | Image for the permission-fixing init container (runs as root, `chown`s mounted PVCs to the agent user). |
| `securityContext` | object | no | `runAsUser: 1001`, `runAsGroup: 1001` | User/group the agent runs as (`runAsUser`, `runAsGroup`, `fsGroup`) and `privileged`. |
| `certTrustStore` | []object | no | — | TLS Secrets mounted into the agent as trusted CAs at `/etc/ssl/certs/<secretName>.crt`. Each entry: `secretName` (the Secret must contain `tls.crt`). |
| `extraEnv` | []object | no | — | Extra env vars for the agent container. Each entry: `name`, and exactly one of `value` or `valueFrom` (`secretKeyRef` / `configMapKeyRef`). |
| `storage` | []object | no | — | Persistent volumes for the agent pods. See [Storage](#storage). |

### Defaults

The mutating webhook applies these when a field is empty or zero:

| Field | Defaulted to |
|---|---|
| `metadata.labels["managed-by"]` | `azdo-runner-operator` |
| `pool` | `default` |
| `image` | `ghcr.io/mahmoudk1000/kara/agent:latest` |
| `imagePullPolicy` | `IfNotPresent` |
| `ttlIdleSeconds` | `300` |
| `maxAgents` (when `minAgents` and `maxAgents` are both 0) | `5` |
| `maxAgents` (when only it is 0) | `max(1, minAgents)` |
| `storage[].size` (when `createPvc: true`) | `1Gi` |

> With the webhooks disabled, zero means "unset" in the API but no defaulting happens — set the fields explicitly.

## Validation rules

The validating webhook rejects:

- `azUrl` that is not an HTTP(S) URL.
- Missing `patSecretName`.
- `image` containing spaces or tabs.
- `imagePullPolicy` outside `Always` / `IfNotPresent` / `Never`.
- Negative `ttlIdleSeconds` or `minAgents`, or `maxAgents < 1`.
- `minAgents > maxAgents`.
- `pollIntervalSeconds < 5`.
- `extraEnv` entries with a missing or invalid name, duplicate names, or both/neither of `value` and `valueFrom`.
- `storage` entries with a missing/duplicate/invalid name or mount path, a relative mount path, `createPvc: true` without a valid `size` (units `Gi`, `Mi`, `Ki` or a plain byte count), an invalid `storageClass` name, or `createPvc: false` with `deleteWithAgent: true`.
- `certTrustStore` entries with a missing, duplicate or invalid `secretName`.

## Scaling behavior

- **Scale up** — one agent pod per queued job request, up to `maxAgents`, minus the agents already registered. Jobs that already have a reserved agent are not counted.
- **Minimum agents** — if fewer than `minAgents` KARA-managed agents exist, more are created.
- **Capability-aware mode** — the operator groups queued jobs by their first demand and only creates agents whose `capabilityImages` entry matches the demand. Jobs with no demand or an unknown demand fall back to the default `image`.
- **Idle scale-down** — an agent whose last completed request is older than `ttlIdleSeconds` (and which is offline or has no active pod) is unregistered and its pod/PVCs are deleted. `minAgents` agents are exempt from idle scale-down.
- **Completed retention** — a pod that just finished a job is kept for the TTL window before cleanup.
- **One-shot mode** — with `ttlIdleSeconds: 0`, non-minimum agents start with `--once`: they unregister and exit as soon as they are idle.
- **Error cleanup** — pods stuck in error states and orphaned pods that the pool no longer reports are reaped.

## The agent pod

KARA names agent pods `<pool>-agent-<index>` (index allocated per pool) and labels them so the finalizer and status can find them. The container:

- Runs the Azure DevOps agent with these environment variables:

  | Variable | Source |
  |---|---|
  | `AZP_URL` | `spec.azUrl` |
  | `AZP_POOL` | `spec.pool` |
  | `AZP_TOKEN` | Secret `patSecretName` key `patSecretKey` |
  | `AZP_AGENT_NAME` | the pod name (the agent registers under this name) |
  | `AZP_CAPABILITY` | the matched capability (capability-aware mode) |
  | `Agent.<name>` | each entry of `spec.capabilities` |
  | — | plus every `spec.extraEnv` entry |

- Mounts `/workspace` (an emptyDir by default; see [Storage](#storage)).
- Mounts each `certTrustStore` Secret at `/etc/ssl/certs/<secretName>.crt`.
- Runs a `preStop` that terminates `Agent.Listener` gracefully before the pod goes away.
- Runs the optional permission-fixing **init container** as root when PVC-backed storage is configured, so the agent user can write to the volumes.

## Storage

`spec.storage` is a list of volumes. Each entry:

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Logical name. The backing PVC is named `<pool>-agent-<index>-<name>`. |
| `mountPath` | string | yes | Absolute path inside the agent container. |
| `size` | string | with `createPvc` | Requested size, e.g. `10Gi`, `500Mi`. |
| `storageClass` | string | no | Storage class for created PVCs (empty = cluster default). |
| `claimName` | string | no | Reference an existing PVC instead of creating one. |
| `createPvc` | bool | no | `true` — KARA creates the PVC (and reuses an existing one with matching labels). `false` — use `claimName`. |
| `optional` | bool | no | `true` — pod creation proceeds even if the PVC cannot be created. |
| `deleteWithAgent` | bool | no | Delete the PVC when its agent is removed. Only valid with `createPvc: true`. |

PVCs are created in the RunnerPool's namespace, owned by the operator and deleted when the RunnerPool is deleted.

## Status

| Field | Description |
|---|---|
| `conditions` | `Ready` and `Error` conditions with reason/message/transition time. |
| `connectionStatus` | `Connected`, `Disconnected` or `Error`. |
| `organizationName` | Organization derived from `azUrl`. |
| `poolName` | The resolved pool name. |
| `active` | `true` while the pool is connected and being reconciled. |
| `queuedJobs` | Jobs currently waiting in the pool queue. |
| `runningAgents` | KARA-managed agents registered in the pool. |
| `agentsSummary` | `<agents>/<maxAgents>` for a quick `kubectl get` glance. |
| `currentAgentIndex` | Next agent index to be allocated. |
| `lastPolled` | Last successful poll timestamp. |
| `lastError` | Last connection/reconciliation error, if any. |
| `agents` | Per-agent detail: `id`, `name`, `status` (`online`/`offline`/`running`), `createdAt`, `lastActive`. |
| `agentIndexes` | Map of in-use index → `podName`, `status`, `isMinAgent`, `createdAt`, `pvcNames`. |
| `queuedJobsByCap` | Queued jobs per demanded capability (capability-aware mode). |

Printed columns:

```
NAME      STATUS       POOL     ORGANIZATION   QUEUED   AGENTS   RUNNING
default   Connected    Default  my-org         2        2/5      2
```
