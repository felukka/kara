# FAQ

## How is this different from `actions/runner-controller` or the Azure DevOps runner autoscaler?

Those scale a pool by managing a fleet of VMs or a single long-lived set of agents. KARA treats each agent as a disposable Kubernetes pod: it is created for work, registered with the pool, and deleted (unregistered + pod + PVCs) when it goes idle. There is no VM lifecycle, no host pool to maintain, and the "node" is just a pod in your existing cluster.

## Can the pool scale to zero?

Yes. Set `minAgents: 0`. With the default `ttlIdleSeconds` the last agent lingers for 5 minutes after its job, then it is unregistered and deleted. Set `ttlIdleSeconds: 0` for immediate teardown (one-shot agents).

## What happens when I delete the RunnerPool?

The finalizer unregisters every KARA-managed agent from the Azure DevOps pool, then deletes all agent pods and PVCs. Only after that does the resource disappear. Agents that were *not* created by KARA (manually registered, or from another operator) are left alone.

## Can I run multiple pools from one operator?

Yes — create one `RunnerPool` per Azure DevOps pool. They are independent: each has its own queue, bounds, image, storage and poll interval, and they scale down to zero individually.

## Are the agent pods scheduled on any particular nodes?

Not by default. Use the standard `nodeSelector` / `affinity` / `tolerations` on the `RunnerPool` if you want agents pinned to specific nodes (for example, a node with a GPU or a specific OS).

## Do I need a StorageClass?

Only if you use `spec.storage` with `createPvc: true` and want a specific class. An empty `storageClass` uses the cluster default. Without `spec.storage`, agent pods use an `emptyDir` for `/workspace` and no PVCs are created.

## Is the PAT stored in the pod spec?

No. The agent container references the Secret via `env.valueFrom.secretKeyRef`, so the token is injected by the kubelet at runtime and never appears in the pod manifest, the `RunnerPool` resource, or `kubectl get pod -o yaml` output beyond the reference.

## What do the agent indices mean?

Each pool allocates agent indices (`0`, `1`, `2`, ...) as it creates agents. The index is stable for the lifetime of an agent, is used in pod and PVC names (`<pool>-agent-<index>`, `<pool>-agent-<index>-<name>`), and is reported in `status.agentIndexes`. Freed indices can be reused.

## Does KARA manage the Azure DevOps pool itself (create/delete it)?

No. KARA manages the *agents inside* an existing pool. You create the pool in Azure DevOps; KARA registers and unregisters agents in it.

## Why `azdo-runner-operator` in some labels?

The `managed-by` label value (`azdo-runner-operator`) and a couple of internal label values are kept from the project's earlier name for continuity. They are cosmetic and do not affect behavior.

## Can I use this with Azure DevOps Server (on-prem)?

Yes — set `azUrl` to your server's collection URL. The same PAT-based flow applies.

## Is there a UI?

Not yet. Everything is driven by `RunnerPool` resources and observable through `kubectl` and the metrics endpoint.
